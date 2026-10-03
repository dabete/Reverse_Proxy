package main

import (
	"context"
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
	backends []string // will store baseURL
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

func main() {

	lb := &LoadBalancer{}
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// implement load balancing and send r to one of the gateways

		// implement load balancing and re
		lb.mu.Lock()

		// see if there are any gateways available and return if not
		if len(lb.backends) == 0 {
			lb.mu.Unlock()
			http.Error(w, "No gateways currently available", http.StatusServiceUnavailable)
			return
		}

		gatewayServiceNumber := lb.counter % len(lb.backends)
		gatewayURL := lb.backends[gatewayServiceNumber] // e.g. "localhost:8080"

		// update lb variables which needs updating
		lb.counter = lb.counter + 1
		lb.mu.Unlock()

		// what happens if ctx times out?
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		outReq, err := createForwardedRequest(ctx, gatewayURL, r)
		if err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		// send the request to the gateway
		resp, err := http.DefaultClient.Do(outReq)
		if err != nil {
			http.Error(w, "There was an error", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

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
