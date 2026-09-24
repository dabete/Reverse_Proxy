package main

import (
	"io"
	"log"
	"net/http"
)

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
	mux := http.NewServeMux()
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
		response, err := http.DefaultClient.Do(forwarded_request)

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

	log.Fatal(http.ListenAndServe(":8080", mux))
}

// need to clone the request
// forward the request
// read the response: wait for the backend server to respond, gives you a status code and body
// copy back to the client, stream its response body straight into the client's ResponseWriter
