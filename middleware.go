package main

import (
	"net"
	"net/http"
	"sync"
	"time"
)

type Middleware func(http.Handler) http.Handler

var middlewares = []Middleware{
	//rateLimitingMiddleware, //remember that it executes in reverse order
	fixedWindow,
}

// rate limiting algorithms:
// leaky bucket
// token bucket
// fixed window counter
// sliding window log
// sliding window counter

type counter struct {
	mu    sync.Mutex
	state map[string]*clientState // do i need to initalise this to a default value?
	limit int                     // requests per ten seconds
}

type clientState struct {
	counter       int
	currentWindow int64
}

var inMemoryCounter = &counter{limit: 5, state: make(map[string]*clientState)}

//var inMemoryClientState = &clientState{counter: 0} - have to initalise on a per client basis

func rateLimitingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CODE BEFORE
		// Runs when the request arrives

		next.ServeHTTP(w, r) // HANDOFF

		// CODE AFTER
		// Runs when the backend finishes responding
	})
}

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

func fixedWindow(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 5 requests per 10 seconds to test

		// extract identity
		identity := headerHelper(r)

		inMemoryCounter.mu.Lock()

		// check if client already exists in map
		value, exists := inMemoryCounter.state[identity]
		if !exists {
			// key does not exist, create a new one
			value = &clientState{
				counter:       0,
				currentWindow: time.Now().Unix() / 10,
			}
			inMemoryCounter.state[identity] = value
		}

		// check the counter and time to possibly reset the counter

		nowWindow := time.Now().Unix() / 10
		if nowWindow != inMemoryCounter.state[identity].currentWindow {
			inMemoryCounter.state[identity].currentWindow = nowWindow
			inMemoryCounter.state[identity].counter = 0
		}

		if inMemoryCounter.state[identity].counter >= inMemoryCounter.limit {
			// reject request
			inMemoryCounter.mu.Unlock()
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		} else {
			inMemoryCounter.state[identity].counter = inMemoryCounter.state[identity].counter + 1
		}

		inMemoryCounter.mu.Unlock()

		// ------ send the request off ------
		next.ServeHTTP(w, r)
	})
}
