package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadyButProtocolUnavailable(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{http.MethodGet, "/health/ready", http.StatusNoContent, ""},
		{http.MethodPost, "/csil/v1/rpc", http.StatusServiceUnavailable, failureBody},
		{http.MethodGet, "/unknown", http.StatusNotFound, "404 page not found\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			r := httptest.NewRecorder()
			handler().ServeHTTP(r, httptest.NewRequest(tc.method, tc.path, nil))
			if r.Code != tc.status || r.Body.String() != tc.body {
				t.Fatalf("got status %d and body %q", r.Code, r.Body.String())
			}
		})
	}
}
