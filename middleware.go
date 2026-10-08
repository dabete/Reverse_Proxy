package main

import (
	"context"
	"log" //apparently needed for splitting the header x forwarded
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type Middleware func(http.Handler) http.Handler

var middlewares = []Middleware{
	//tokenBucket,
}

type clientState struct {
	tokens    float64
	timestamp time.Time
}

type clientManager struct {
	mu                sync.Mutex
	limit             float64
	tokens_per_second float64
	state             map[string]*clientState
}

var manager = &clientManager{
	limit:             5,
	tokens_per_second: 1,
	state:             make(map[string]*clientState),
}

//var inMemoryCounter = &counter{limit: 5, state: make(map[string]*clientState)}

//var inMemoryClientState = &clientState{counter: 0} - have to initalise on a per client basis

var tokenBucketScript = redis.NewScript(`
		local key = KEYS[1]
		local capacity = tonumber(ARGV[1])
		local tokens_per_second = tonumber(ARGV[2])
		local ttl = tonumber(ARGV[3])

		local time = redis.call("TIME")
		local now = tonumber(time[1]) + tonumber(time[2]) / 1e6 -- ask about this line order of operations
		
		
		local data = redis.call("HMGET", key, "tokens", "timestamp")
		
		local tokens = tonumber(data[1])
		local last_refill = tonumber(data[2])
		
		if tokens == nil then -- user doesn't exist, give max tokens
			tokens = capacity
			last_refill = now
		else
			local elapsed = math.max(0, now - last_refill)
			tokens = math.min(capacity, tokens + (elapsed * tokens_per_second))
			last_refill = now
		end

		if tokens < 1.0 then
			redis.call("HSET", key, "tokens", tokens, "timestamp", last_refill)
			redis.call("EXPIRE", key, ttl)
			return 0
		end

		tokens = tokens - 1.0
		redis.call("HSET", key, "tokens", tokens, "timestamp", last_refill)
		redis.call("EXPIRE", key, ttl)
		return 1
		`)

func redisTokenBucketRateLimiting(rdb *redis.Client, capacity, rate float64, ttl int) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

			if r.URL.Path == "/health" { // does not need rate limiting
				next.ServeHTTP(w, r)
				return
			}

			// TOKEN_LIMIT := 5.0
			// TOKENS_PER_SECOND := 1.0
			// TTL := 100 // seconds I believe

			identity := headerHelper(r)
			key := "rate_limit:" + identity // look into this change, they changed the helper header file anyways so may have to change this

			ctx, cancel := context.WithTimeout(r.Context(), 250*time.Millisecond)
			defer cancel()

			allowed, err := tokenBucketScript.Run(
				ctx,
				rdb,
				[]string{key},
				capacity,
				rate,
				ttl).Int()

			if err != nil {
				log.Printf("rate limiter error, failing open: %v", err)
				next.ServeHTTP(w, r)
				return
			}

			if allowed == 0 {
				w.Header().Set("Retry-After", "1") // check why this is useful
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r) // HANDOFF
		})
	}
}

func rateLimitingMiddlewareTemplate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CODE BEFORE
		// Runs when the request arrives

		next.ServeHTTP(w, r) // HANDOFF

		// CODE AFTER
		// Runs when the backend finishes responding
	})
}

// X-Forwarded-For is a plaintext header, anyone can write anything in this which is why it is a vulnerability
// Trust is a question about the sender, not the header
// The header is only believeable if it was written by someone you trust
// A load balancer is trustworthy, an attacker is not
// This is why it is important to look at RemoteAddr first, even in the case you end up using the header
// It's the question "who is talking to me directly?", and the answer decides whether the header is worth reading
// need to change this function and update it --- IMPORTANT COME BACK TO THISISSSSSSS
func headerHelper(r *http.Request) string {
	// job of this helper function is to extract the header 'X-Forwarded-For"
	// if request doesn't contain this header then resort back to regular IP address

	address := r.Header.Get("X-Forwarded-For")

	println("DEBUG header value:", address, "RemoteAddr:", r.RemoteAddr)

	if address != "" {
		return address
	}

	// fall back to regular IP address
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return ip
}

func tokenBucket(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		// three steps:
		// fetch the hash
		identity := headerHelper(r)

		manager.mu.Lock()

		// check if client already exists in map
		value, exists := manager.state[identity]
		if !exists {
			// key does not exist, create a new one
			value = &clientState{
				tokens:    manager.limit,
				timestamp: time.Now(),
			}
			manager.state[identity] = value
		}

		// refill the bucket
		// calculate seconds since last timestamp
		nowTimestamp := time.Now()
		elapsed := nowTimestamp.Sub(value.timestamp).Seconds()
		value.timestamp = nowTimestamp

		tokens_to_refill := elapsed * manager.tokens_per_second
		value.tokens = value.tokens + tokens_to_refill

		// check tokens haven't gone over the limit
		if value.tokens > manager.limit {
			value.tokens = manager.limit
		}

		// check the bucket
		if value.tokens < 1.0 {
			manager.mu.Unlock()
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		}

		value.tokens = value.tokens - 1
		manager.mu.Unlock()
		next.ServeHTTP(w, r) // HANDOFF

	})
}
