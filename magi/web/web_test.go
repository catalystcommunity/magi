package web

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestVersionedFiles(t *testing.T) {
	h := Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	page := rec.Body.String()
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("index %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	m := regexp.MustCompile(`src="(/s/[0-9a-f]{16}/js/[^"]+)"`).FindStringSubmatch(page)
	if m == nil || !strings.Contains(page, `href="/s/`) {
		t.Fatalf("index does not use versioned paths: %s", page)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", m[1], nil))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Fatalf("script %d %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/js/magi/web/src/app.js", nil))
	if rec.Code != 404 {
		t.Fatalf("unversioned path %d", rec.Code)
	}
}
