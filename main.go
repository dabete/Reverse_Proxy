package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/redis/go-redis/v9"
)

type GatewayService struct {
	IsAlive       bool   `json:"isAlive"`
	URL           string `json:"Url"`
	HealthCounter int    `json:"HealthCounter"`
}

// add timeout just in case Spring boot freezes
var backendClient = &http.Client{Timeout: 30 * time.Second}

func buildForwardedURL(r *http.Request) string {
	baseURL := "http://localhost:5000"

	// using RequestURI keeps the query parameters intact as opposed to using path
	forwarded_url := baseURL + r.URL.RequestURI()
	return forwarded_url
}

func createForwardedRequest(method string, body io.ReadCloser, url string) (*http.Request, error) {
	return http.NewRequest(method, url, body)
}

func removeHopByHopHeaders(headers http.Header) http.Header {
	hopByHopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}

	for _, header := range hopByHopHeaders {
		headers.Del(header)
	}

	return headers
}

func attachBackendHeaders(w http.ResponseWriter, response *http.Response) {
	for key, values := range response.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
}

func main() {

	// initialise connection to redis database
	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379",
		Password: "", // no password
		DB:       0,  // use default DB
		Protocol: 2,
	})

	// fail immediately if redis isn't reachable
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		log.Fatalf("Cannot connect to redis: %v", err)
	}

	//middlewares = append(middlewares, redisTokenBucketRateLimiting(rdb))

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200) // OK
		response := []byte("Gateway is healthy")
		w.Write(response)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		method := r.Method
		headers := r.Header.Clone()
		body := r.Body

		forwarded_url := buildForwardedURL(r)
		forwarded_request, err := createForwardedRequest(method, body, forwarded_url)
		if err != nil {
			http.Error(w, "Invalid request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// attach the headers
		headers = removeHopByHopHeaders(headers)
		forwarded_request.Header = headers

		// send to the backend
		response, err := backendClient.Do(forwarded_request)

		if err != nil {
			http.Error(w, "Bad Gateway: backend unreachable", http.StatusBadGateway) // status code 502
			return
		}

		defer response.Body.Close() // prevent TCP connection leaks

		attachBackendHeaders(w, response)
		w.WriteHeader(response.StatusCode)
		io.Copy(w, response.Body)

		// sends back response
		// response.StatusCode
		// response.Header (headers from Spring Boot)
		// response.Body (the data stream sent by Spring Boot)
		// err (any network level error e.g. Spring Boot is offline or connection refused)
	})

	var wrappedMux http.Handler = mux
	// wrap in middleware
	for _, middleware := range middlewares {
		wrappedMux = middleware(wrappedMux)
	}

	// wrap with redis rate limiter
	rateLimiter := redisTokenBucketRateLimiting(rdb, 5, 1, 100)
	wrappedMux = rateLimiter(wrappedMux)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	// tell the load balancer that this gateway is alive
	// create json payload
	payload := GatewayService{
		IsAlive:       true,
		URL:           "http://localhost:" + port,
		HealthCounter: 2,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		log.Fatal(http.ListenAndServe(":"+port, wrappedMux))
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	for {
		resp, err := client.Post("http://localhost:9000/admin/register",
			"application/json", bytes.NewBuffer(jsonData))
		if err != nil {
			log.Printf("Could not register with load balancer, will retry in 10 seconds: %v", err)
		} else {
			resp.Body.Close()
		}
		time.Sleep(10 * time.Second)

	}

}

// need to clone the request
// forward the request
// read the response: wait for the backend server to respond, gives you a status code and body
// copy back to the client, stream its response body straight into the client's ResponseWriter

// REMEMBER TO CHANGE WHICH X-FORWARDED-HEADER WE NEED TO LOOK AT
