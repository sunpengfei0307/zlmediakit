package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceTaskMutationsArePostOnly(t *testing.T) {
	r := New("0", "test")
	r.SetupApp()
	paths := []string{
		"/sources/pull/add", "/sources/pull/delete",
		"/sources/pusher/add", "/sources/pusher/delete",
		"/sources/ffmpeg/add", "/sources/ffmpeg/delete",
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		for _, path := range paths {
			t.Run(method+" "+path, func(t *testing.T) {
				req := httptest.NewRequest(method, path, nil)
				rec := httptest.NewRecorder()
				r.engine.ServeHTTP(rec, req)
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
				}
			})
		}
	}
}

func TestSourcesPageAndExplicitMutationRoutesAreRegistered(t *testing.T) {
	r := New("0", "test")
	r.SetupApp()
	want := map[string]string{
		"GET /sources":                http.MethodGet,
		"POST /sources/pull/add":      http.MethodPost,
		"POST /sources/pull/delete":   http.MethodPost,
		"POST /sources/pusher/add":    http.MethodPost,
		"POST /sources/pusher/delete": http.MethodPost,
		"POST /sources/ffmpeg/add":    http.MethodPost,
		"POST /sources/ffmpeg/delete": http.MethodPost,
	}
	seen := map[string]bool{}
	for _, route := range r.engine.Routes() {
		seen[route.Method+" "+route.Path] = true
	}
	for route := range want {
		if !seen[route] {
			t.Fatalf("missing route %s", route)
		}
	}
}
