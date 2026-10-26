// This fixture is only for an approved rollback test on the isolated canary.
// It reports readiness but cannot pass the controller's protocol check.
package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

const failureBody = "intentional Magi rollback drill: protocol unavailable\n"

func handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /csil/v1/rpc", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(failureBody))
	})
	return mux
}

func main() {
	listen := os.Getenv("OPAL_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	s := &http.Server{Addr: listen, Handler: handler(), ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 8192}
	log.Fatal(s.ListenAndServe())
}
