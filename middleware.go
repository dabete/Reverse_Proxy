package main

import (
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
	mu            sync.Mutex
	counter       int
	currentWindow int64
	limit         int // requests per ten seconds
}

var inMemoryCounter = &counter{counter: 0, limit: 5}

func rateLimitingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CODE BEFORE
		// Runs when the request arrives

		next.ServeHTTP(w, r) // HANDOFF

		// CODE AFTER
		// Runs when the backend finishes responding
	})
}

func fixedWindow(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 5 requests per 10 seconds to test

		// implementation
		inMemoryCounter.mu.Lock()
		// check the counter and time to possibly reset the counter
		// elapsed := time.Since(inMemoryCounter.currentTime)
		// if elapsed > 10*time.Second {
		// 	inMemoryCounter.counter = 0
		// 	inMemoryCounter.currentTime = time.Now()
		// }

		nowWindow := time.Now().Unix() / 10
		if nowWindow != inMemoryCounter.currentWindow {
			inMemoryCounter.currentWindow = nowWindow
			inMemoryCounter.counter = 0
		}
		//inMemoryCounter.counter = inMemoryCounter.counter + 1
		if inMemoryCounter.counter >= inMemoryCounter.limit {
			// reject request
			inMemoryCounter.mu.Unlock()
			http.Error(w, "Too many requests", http.StatusTooManyRequests)
			return
		} else {
			inMemoryCounter.counter = inMemoryCounter.counter + 1
		}

		inMemoryCounter.mu.Unlock()

		// ------ send the request off ------
		next.ServeHTTP(w, r)
	})
}
