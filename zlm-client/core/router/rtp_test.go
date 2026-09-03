package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRTPRoutesRegisteredAndMutationsArePostOnly(t *testing.T) {
	r := New("0", "test")
	r.SetupApp()
	mutations := []string{
		"/rtp/open", "/rtp/open-multiplex", "/rtp/connect", "/rtp/close", "/rtp/update-ssrc",
		"/rtp/pause-check", "/rtp/resume-check", "/rtp/start-send", "/rtp/start-send-passive",
		"/rtp/start-send-talk", "/rtp/stop-send",
	}
	seen := map[string]bool{}
	for _, route := range r.engine.Routes() {
		seen[route.Method+" "+route.Path] = true
	}
	if !seen["GET /rtp"] {
		t.Fatal("missing GET /rtp")
	}
	if !seen["GET /protocols"] {
		t.Fatal("missing GET /protocols")
	}
	for _, path := range mutations {
		if !seen["POST "+path] {
			t.Fatalf("missing POST %s", path)
		}
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
			req := httptest.NewRequest(method, path, nil)
			rec := httptest.NewRecorder()
			r.engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s status=%d body=%s", method, path, rec.Code, rec.Body.String())
			}
		}
	}
}
