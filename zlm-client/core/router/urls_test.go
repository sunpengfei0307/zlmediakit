package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSetupAppDoesNotRegisterClockRoute(t *testing.T) {
	r := New("0", "test")
	r.SetupApp()
	for _, route := range r.engine.Routes() {
		if route.Path == "/ui/clock" {
			t.Fatalf("obsolete clock route is still registered: %+v", route)
		}
	}
}

func TestCoreMutationNonPOSTRequestsReturnMethodNotAllowed(t *testing.T) {
	r := New("0", "test")
	r.SetupApp()
	paths := []string{
		"/ui/streams/close",
		"/ui/kick",
		"/ui/sessions/kick",
		"/ui/sessions/kick-selected",
		"/files/record/start",
		"/files/record/stop",
		"/files/vod/loadMP4File",
		"/files/vod/startRecordTask",
		"/files/vod/deleteRecordFile",
		"/files/vod/setRecordSpeed",
		"/files/vod/seekRecordStamp",
		"/files/vod/pauseStream",
		"/files/vod/seekStream",
		"/files/vod/setStreamSpeed",
		"/rtp/open",
		"/rtp/open-multiplex",
		"/rtp/connect",
		"/rtp/close",
		"/rtp/update-ssrc",
		"/rtp/pause-check",
		"/rtp/resume-check",
		"/rtp/start-send",
		"/rtp/start-send-passive",
		"/rtp/start-send-talk",
		"/rtp/stop-send",
		"/onvif-webrtc/scan",
		"/onvif-webrtc/keeper/add",
		"/onvif-webrtc/keeper/delete",
		"/onvif-webrtc/import-pull",
		"/config/advanced/restart",
		"/config/advanced/delete-record-dir",
		"/config/advanced/delete-snap-dir",
		"/config/advanced/broadcast",
		"/api/node/node-1/close_stream",
		"/api/node/node-1/close_streams",
		"/api/node/node-1/kick_session",
		"/api/node/node-1/kick_sessions",
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
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
