package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// For now will make a simple round-robin algorithm

type LoadBalancer struct {
	mu       sync.Mutex
	counter  int
	gateways []*Gateway // will store baseURL
}

type Gateway struct {
	IsAlive       bool   `json:"isAlive"`
	URL           string `json:"Url"`
	HealthCounter int    `json:"HealthCounter"`
	//TwiceInARow   bool   //`json:"TwiceInARow`
}

func checkBackendIsHealthy(gatewayURL string) bool {
	// set a timeout so that an unhealthy backend doesn't leave the request hanging
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", gatewayURL+"/health", nil)
	if err != nil {
		return false
	}

	//transmit the request
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}

	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// add an append x forwarded for method

func normaliseURL(gatewayURL string) string {
	if !strings.HasPrefix(gatewayURL, "http://") && !strings.HasPrefix(gatewayURL, "https://") {
		gatewayURL = "http://" + gatewayURL
	}

	return gatewayURL
}

func buildURL(normalisedGatewayURL string, reqURI string) (string, error) {
	// prevent double / at the end
	target, err := url.Parse(normalisedGatewayURL)
	if err != nil {
		return "", err
	}

	return strings.TrimRight(target.String(), "/") + reqURI, nil
}

func createForwardedRequest(ctx context.Context, gatewayURL string, r *http.Request) (*http.Request, error) {
	// normalise URL
	normalisedGatewayURL := normaliseURL(gatewayURL)
	targetURL, err := buildURL(normalisedGatewayURL, r.URL.RequestURI())
	if err != nil {
		return nil, err
	}

	outReq, err := http.NewRequestWithContext(ctx, r.Method, targetURL, r.Body)
	if err != nil {
		return nil, err
	}

	outReq.Header = r.Header.Clone()

	hopByHop := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailers",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, h := range hopByHop {
		outReq.Header.Del(h)
	}

	// append X-Forwarded-For header to the end
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		// no port found so just pass client IP address as it is
		clientIP = r.RemoteAddr
	}

	prior := r.Header.Get("X-Forwarded-For")
	if prior != "" {
		outReq.Header.Set("X-Forwarded-For", prior+", "+clientIP)
	} else {
		outReq.Header.Set("X-Forwarded-For", clientIP)
	}

	return outReq, nil
}

func startPolling(lb *LoadBalancer) {
	ticker := time.NewTicker(5 * time.Second)

	go func() {
		defer ticker.Stop()

		for range ticker.C {
			fmt.Println("Polling check performed at:", time.Now().Format("15:04:05"))
			var deadGateways []*Gateway
			lb.mu.Lock()
			for _, gateway := range lb.gateways {
				if gateway.IsAlive == false {
					deadGateways = append(deadGateways, gateway)
				}
			}
			lb.mu.Unlock()

			// perform HTTP health check
			for _, gateway := range deadGateways {
				resp, err := http.Get(gateway.URL + "/health")
				if err != nil {
					log.Fatalf("Request Failed: %v", err)
				}
				defer resp.Body.Close()

				if resp.StatusCode == 200 {
					lb.mu.Lock()
					gateway.IsAlive = true
					gateway.HealthCounter = 2
					lb.mu.Unlock()
				}
			}

		}
	}()
}

func main() {

	lb := &LoadBalancer{}
	startPolling(lb)
	mux := http.NewServeMux()

	mux.HandleFunc("POST /admin/register", func(w http.ResponseWriter, r *http.Request) {
		// will first check if an instance of it already appears
		var newGateway Gateway

		err := json.NewDecoder(r.Body).Decode(&newGateway)
		if err != nil {
			http.Error(w, "Bad JSON format", http.StatusBadRequest)
			return
		}

		exists := false
		lb.mu.Lock()
		for _, gateway := range lb.gateways {
			if gateway.URL == newGateway.URL {
				gateway.IsAlive = true
				//gateway.TwiceInARow = false
				exists = true
			}
		}

		// if doesn't appear append new object of struct backend
		//newGateway.TwiceInARow = false
		if exists == false {
			lb.gateways = append(lb.gateways, &newGateway)
		}
		lb.mu.Unlock()

		w.WriteHeader(http.StatusOK)

	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {

		lb.mu.Lock()

		var activeGateways []*Gateway

		// make sub-list of arrays
		for _, gateway := range lb.gateways {
			if gateway.IsAlive == true {
				activeGateways = append(activeGateways, gateway)
			}
		}

		// see if there are any gateways available and return if not

		if len(activeGateways) == 0 {
			lb.mu.Unlock()
			http.Error(w, "No gateways currently available", http.StatusServiceUnavailable)
			return
		}

		gatewayServiceNumber := lb.counter % len(activeGateways)
		gateway := activeGateways[gatewayServiceNumber] // e.g. "http://localhost:8080"

		// update lb variables which needs updating
		lb.counter = lb.counter + 1
		lb.mu.Unlock()

		// what happens if ctx times out?
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		outReq, err := createForwardedRequest(ctx, gateway.URL, r)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		// send the request to the gateway
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			// perform gateway health check
			if gateway.HealthCounter == 2 {
				gateway.HealthCounter = gateway.HealthCounter - 1
			} else if gateway.HealthCounter == 1 {
				gateway.IsAlive = false
			}

			http.Error(w, "There was an error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// if request went through reset health check
		gateway.HealthCounter = 2

		for key, values := range resp.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}

		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)

	})

	log.Fatal(http.ListenAndServe(":9000", mux))
}
