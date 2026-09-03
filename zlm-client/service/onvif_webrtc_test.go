package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"zlm-admin/core/config"
)

func newOnvifWebRTCHub(t *testing.T, response func(string) string) (*Hub, *recordingAudit, *[]sourceCall) {
	t.Helper()
	var mu sync.Mutex
	calls := make([]sourceCall, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		mu.Lock()
		calls = append(calls, sourceCall{api: api, method: r.Method, form: r.Form})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response(api)))
	}))
	t.Cleanup(srv.Close)
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	audit := &recordingAudit{}
	return &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}, audit, &calls
}

func TestOnvifScanUsesExactPOSTParametersAndIsolatesMalformedDevices(t *testing.T) {
	h, audit, calls := newOnvifWebRTCHub(t, func(string) string {
		return `{"code":0,"data":[{"onvif_url":"http://10.0.0.2/onvif/device_service","location":"Lab","name":"Camera A","hardware":"IPC-X"},"bad",{"name":"missing URL"}]}`
	})
	got := h.SearchOnvifDevices("node-1", "admin", url.Values{
		"timeout_ms": {"500"}, "subnet_prefix": {"10.0.0.0/24"},
	})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	devices, _ := got["devices"].([]map[string]any)
	if len(devices) != 1 || devices[0]["onvif_url"] != "http://10.0.0.2/onvif/device_service" {
		t.Fatalf("devices=%+v", devices)
	}
	if len(*calls) != 1 || (*calls)[0].api != "searchOnvifDevice" ||
		(*calls)[0].method != http.MethodPost || (*calls)[0].form.Get("timeout_ms") != "500" ||
		(*calls)[0].form.Get("subnet_prefix") != "10.0.0" {
		t.Fatalf("calls=%+v", *calls)
	}
	entries := audit.List()
	if len(entries) != 2 || entries[0].OperationID == "" ||
		entries[0].OperationID != entries[1].OperationID ||
		entries[0].Phase != "intent" || entries[1].Phase != "result" {
		t.Fatalf("audit=%+v", entries)
	}
}

func TestOnvifScanValidationAndAuditFailClosed(t *testing.T) {
	for _, q := range []url.Values{
		{"timeout_ms": {"499"}},
		{"timeout_ms": {"10001"}},
		{"timeout_ms": {"abc"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"http://10.0.0.0/24"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"10.0.0.\n"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"10.0.0."}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"10..0.1"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"256.0.0"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"10.0"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"10.0.0.0/25"}},
		{"timeout_ms": {"500"}, "subnet_prefix": {"10.0.0.0/33"}},
	} {
		h, audit, calls := newOnvifWebRTCHub(t, func(string) string { return `{"code":0,"data":[]}` })
		got := h.SearchOnvifDevices("node-1", "admin", q)
		if asFloat(got["code"]) == 0 || len(*calls) != 0 || len(audit.List()) != 1 {
			t.Fatalf("q=%v result=%+v calls=%+v audit=%+v", q, got, *calls, audit.List())
		}
	}

	h, _, calls := newOnvifWebRTCHub(t, func(string) string { return `{"code":0,"data":[]}` })
	h.audit = nil
	got := h.SearchOnvifDevices("node-1", "admin", url.Values{"timeout_ms": {"500"}})
	if asFloat(got["code"]) == 0 || len(*calls) != 0 || !strings.Contains(asString(got["msg"]), "审计") {
		t.Fatalf("result=%+v calls=%+v", got, *calls)
	}
}

func TestOnvifScanNormalizesAcceptedSubnetFormsToThreeOctets(t *testing.T) {
	for input, want := range map[string]string{
		"192.168.1":       "192.168.1",
		"192.168.1.42":    "192.168.1",
		"192.168.1.99/24": "192.168.1",
		" 192.168.001 ":   "",
	} {
		t.Run(input, func(t *testing.T) {
			h, _, calls := newOnvifWebRTCHub(t, func(string) string { return `{"code":0,"data":[]}` })
			got := h.SearchOnvifDevices("node-1", "admin", url.Values{
				"timeout_ms": {"10000"}, "subnet_prefix": {input},
			})
			if want == "" {
				if asFloat(got["code"]) == 0 || len(*calls) != 0 {
					t.Fatalf("input=%q result=%+v calls=%+v", input, got, *calls)
				}
				return
			}
			if asFloat(got["code"]) != 0 || len(*calls) != 1 ||
				(*calls)[0].form.Get("subnet_prefix") != want {
				t.Fatalf("input=%q result=%+v calls=%+v", input, got, *calls)
			}
		})
	}
}

func TestOnvifScanDeadlineCoversRequestedTimeoutPlusNetworkMargin(t *testing.T) {
	var remaining time.Duration
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Fatal("scan request has no deadline")
		}
		remaining = time.Until(deadline)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":[]}`)),
			Header:     make(http.Header),
		}, nil
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid", Secret: "secret"})
	h := &Hub{
		zlm:   &zlmClient{http: &http.Client{Transport: transport, Timeout: 4 * time.Second}},
		audit: &recordingAudit{},
	}
	got := h.SearchOnvifDevices("node-1", "admin", url.Values{"timeout_ms": {"10000"}})
	if asFloat(got["code"]) != 0 {
		t.Fatalf("result=%+v", got)
	}
	if remaining < 10*time.Second {
		t.Fatalf("scan deadline was truncated: %s", remaining)
	}
}

func TestWebRTCReadSectionsUseRealDataAndKeepErrorsLocal(t *testing.T) {
	h, _, calls := newOnvifWebRTCHub(t, func(api string) string {
		switch api {
		case "listWebrtcRooms":
			return `{"code":0,"data":[{"room_id":"room-a","peer_count":2},"bad"]}`
		case "listWebrtcRoomKeepers":
			return `{"code":-1,"msg":"ENABLE_WEBRTC unavailable"}`
		case "getWebrtcProxyPlayerInfo":
			return `{"code":0,"msg":"success","data":{"stream_key":"proxy-a","bytes_usage":1234,"transport":"udp"}}`
		default:
			return `{"code":-1}`
		}
	})
	view := h.ListOnvifWebRTC("node-1", "proxy-a")
	if len(view.Rooms.Items) != 1 || view.Rooms.Items[0]["room_id"] != "room-a" {
		t.Fatalf("rooms=%+v", view.Rooms)
	}
	if !strings.Contains(view.Keepers.Error, "ENABLE_WEBRTC") || view.Player.Error != "" ||
		view.Player.Item["stream_key"] != "proxy-a" {
		t.Fatalf("view=%+v", view)
	}
	want := []string{"listWebrtcRooms", "listWebrtcRoomKeepers", "getWebrtcProxyPlayerInfo"}
	for i, api := range want {
		if (*calls)[i].api != api || (*calls)[i].method != http.MethodGet {
			t.Fatalf("calls=%+v", *calls)
		}
	}
	if (*calls)[2].form.Get("key") != "proxy-a" {
		t.Fatalf("detail params=%v", (*calls)[2].form)
	}
}

func TestWebRTCPlayerKeyValidationPreventsUpstreamCall(t *testing.T) {
	for _, key := range []string{"../x", "x\nbad", "", strings.Repeat("x", 1025)} {
		h, _, calls := newOnvifWebRTCHub(t, func(string) string { return `{"code":0,"data":[]}` })
		view := h.ListOnvifWebRTC("node-1", key)
		if key != "" && view.Player.Error == "" {
			t.Fatalf("key=%q view=%+v", key, view)
		}
		for _, call := range *calls {
			if call.api == "getWebrtcProxyPlayerInfo" {
				t.Fatalf("unsafe detail call key=%q calls=%+v", key, *calls)
			}
		}
	}
}

func TestRoomKeeperMutationsUsePOSTValidateDataAndPairAudit(t *testing.T) {
	for _, tc := range []struct {
		action string
		q      url.Values
		api    string
	}{
		{WebRTCRoomKeeperAdd, url.Values{"server_host": {"rtc.example.test"}, "server_port": {"65535"}, "room_id": {"room-a"}, "ssl": {"1"}}, "addWebrtcRoomKeeper"},
		{WebRTCRoomKeeperDelete, url.Values{"room_key": {"keeper-a"}}, "delWebrtcRoomKeeper"},
	} {
		h, audit, calls := newOnvifWebRTCHub(t, func(api string) string {
			if api == "addWebrtcRoomKeeper" {
				return `{"code":0,"msg":"success","data":{"room_key":"keeper-a"}}`
			}
			return `{"code":0}`
		})
		got := h.WebRTCRoomKeeperOperation("node-1", "admin", tc.action, tc.q)
		if asFloat(got["code"]) != 0 || len(*calls) != 1 || (*calls)[0].api != tc.api ||
			(*calls)[0].method != http.MethodPost {
			t.Fatalf("action=%s result=%+v calls=%+v", tc.action, got, *calls)
		}
		entries := audit.List()
		if len(entries) != 2 || entries[0].OperationID == "" || entries[0].OperationID != entries[1].OperationID {
			t.Fatalf("audit=%+v", entries)
		}
	}
}

func TestRoomKeeperRejectsUnsafeFieldsAndMissingAddKey(t *testing.T) {
	bad := []url.Values{
		{"server_host": {"https://rtc.example"}, "server_port": {"80"}, "room_id": {"room"}, "ssl": {"0"}},
		{"server_host": {"user:pass@rtc.example"}, "server_port": {"80"}, "room_id": {"room"}, "ssl": {"0"}},
		{"server_host": {"rtc.example/path"}, "server_port": {"80"}, "room_id": {"room"}, "ssl": {"0"}},
		{"server_host": {"rtc.example"}, "server_port": {"0"}, "room_id": {"room"}, "ssl": {"0"}},
		{"server_host": {"rtc.example"}, "server_port": {"80"}, "room_id": {"../room"}, "ssl": {"0"}},
		{"server_host": {"rtc.example"}, "server_port": {"80"}, "room_id": {"room"}, "ssl": {"2"}},
	}
	for _, q := range bad {
		h, _, calls := newOnvifWebRTCHub(t, func(string) string { return `{"code":0,"data":{"room_key":"x"}}` })
		got := h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperAdd, q)
		if asFloat(got["code"]) == 0 || len(*calls) != 0 {
			t.Fatalf("q=%v result=%+v calls=%+v", q, got, *calls)
		}
	}
	h, audit, _ := newOnvifWebRTCHub(t, func(string) string { return `{"code":0,"data":{}}` })
	got := h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperAdd, url.Values{
		"server_host": {"rtc.example"}, "server_port": {"443"}, "room_id": {"room"}, "ssl": {"1"},
	})
	if asFloat(got["code"]) == 0 || len(audit.List()) != 2 || audit.List()[1].Success {
		t.Fatalf("result=%+v audit=%+v", got, audit.List())
	}
}

func TestRoomKeeperNormalizesIPv6AndRejectsBrokenBrackets(t *testing.T) {
	for input, want := range map[string]string{
		"2001:0db8::1":   "2001:db8::1",
		"[2001:0db8::1]": "[2001:db8::1]",
		"192.0.2.8":      "192.0.2.8",
		"rtc.example":    "rtc.example",
	} {
		t.Run(input, func(t *testing.T) {
			h, _, calls := newOnvifWebRTCHub(t, func(string) string {
				return `{"code":0,"data":{"room_key":"keeper-v6"}}`
			})
			got := h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperAdd, url.Values{
				"server_host": {input}, "server_port": {"443"}, "room_id": {"room"}, "ssl": {"1"},
			})
			if asFloat(got["code"]) != 0 || len(*calls) != 1 ||
				(*calls)[0].form.Get("server_host") != want {
				t.Fatalf("input=%q result=%+v calls=%+v", input, got, *calls)
			}
		})
	}
	for _, input := range []string{"[2001:db8::1", "2001:db8::1]", "[[2001:db8::1]]", "[]"} {
		t.Run("reject "+input, func(t *testing.T) {
			h, _, calls := newOnvifWebRTCHub(t, func(string) string {
				return `{"code":0,"data":{"room_key":"keeper-v6"}}`
			})
			got := h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperAdd, url.Values{
				"server_host": {input}, "server_port": {"443"}, "room_id": {"room"}, "ssl": {"1"},
			})
			if asFloat(got["code"]) == 0 || len(*calls) != 0 {
				t.Fatalf("input=%q result=%+v calls=%+v", input, got, *calls)
			}
		})
	}
}

func TestOnvifAndRoomKeeperFinalAuditFailureDoesNotReportSuccessOrRetry(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		response string
		run      func(*Hub) map[string]any
	}{
		{
			name: "scan", action: "searchOnvifDevice", response: `{"code":0,"data":[]}`,
			run: func(h *Hub) map[string]any {
				return h.SearchOnvifDevices("node-1", "admin", url.Values{"timeout_ms": {"500"}})
			},
		},
		{
			name: "keeper add", action: WebRTCRoomKeeperAdd,
			response: `{"code":0,"data":{"room_key":"keeper-a"}}`,
			run: func(h *Hub) map[string]any {
				return h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperAdd, url.Values{
					"server_host": {"rtc.example"}, "server_port": {"443"},
					"room_id": {"room-a"}, "ssl": {"1"},
				})
			},
		},
		{
			name: "keeper delete", action: WebRTCRoomKeeperDelete, response: `{"code":0}`,
			run: func(h *Hub) map[string]any {
				return h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperDelete, url.Values{
					"room_key": {"keeper-a"},
				})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if api := strings.TrimPrefix(r.URL.Path, "/index/api/"); api != tc.action {
					t.Fatalf("unexpected API %s", api)
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			defer srv.Close()
			withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
			audit := &failingAudit{failOn: 2}
			h := &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}
			got := tc.run(h)
			if asFloat(got["code"]) == 0 ||
				!strings.Contains(asString(got["msg"]), "上游可能已执行") {
				t.Fatalf("result=%+v", got)
			}
			if calls != 1 {
				t.Fatalf("upstream action executed %d times", calls)
			}
			entries := audit.List()
			if len(entries) != 1 || entries[0].Phase != "intent" || entries[0].OperationID == "" {
				t.Fatalf("audit=%+v", entries)
			}
		})
	}
}

func TestOnvifWebRTCErrorsRedactSecretsAndUnexpectedUserinfo(t *testing.T) {
	const secret = "transport-secret"
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("down %s user:pass@rtc.example ?secret=%s", r.URL.String(), secret)
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid", Secret: secret})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: audit}
	got := h.WebRTCRoomKeeperOperation("node-1", "admin", WebRTCRoomKeeperAdd, url.Values{
		"server_host": {"rtc.example"}, "server_port": {"443"}, "room_id": {"room"}, "ssl": {"1"},
	})
	blob := fmt.Sprint(got, audit.List())
	for _, leaked := range []string{secret, "user:pass", "secret="} {
		if strings.Contains(blob, leaked) {
			t.Fatalf("leaked %q in %s", leaked, blob)
		}
	}
}

func TestImportOnvifPullReusesSourceTaskOperationAndRedactsFailedRequest(t *testing.T) {
	h, audit, calls := newOnvifWebRTCHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":-1,"msg":"cannot pull camera-user password camera-pass","data":{"url":"rtsp://camera-user:camera-pass@10.0.0.2/live","password":"camera-pass"},"debug":[{"credential":"camera-user:camera-pass@"}]}`
	})
	got := h.ImportOnvifPull("node-1", "admin", url.Values{
		"url": {"rtsp://camera-user:camera-pass@10.0.0.2/live"},
		"app": {"live"}, "stream": {"camera"},
	}, true)
	if asFloat(got["code"]) == 0 || len(*calls) != 2 ||
		(*calls)[0].api != "listStreamProxy" || (*calls)[1].api != "addStreamProxy" {
		t.Fatalf("result=%+v calls=%+v", got, *calls)
	}
	if _, exists := got["zlm_response"]; exists {
		t.Fatalf("page result exposed raw zlm_response: %+v", got)
	}
	for key := range got {
		if key != "code" && key != "msg" && key != "key" {
			t.Fatalf("non-whitelisted page result key %q: %+v", key, got)
		}
	}
	blob, _ := json.Marshal(map[string]any{
		"result": got, "audit": audit.List(), "notice": asString(got["msg"]),
	})
	for _, leaked := range []string{"camera-user:camera-pass@", "camera-pass", "user:password@"} {
		if strings.Contains(string(blob), leaked) {
			t.Fatalf("credential %q leaked in full object: %s", leaked, blob)
		}
	}
}

func TestImportOnvifPullOnlyAllowsRTSPAndAuditsRejection(t *testing.T) {
	h, audit, calls := newOnvifWebRTCHub(t, func(string) string { return `{"code":0}` })
	got := h.ImportOnvifPull("node-1", "admin", url.Values{
		"url": {"http://10.0.0.2/live"}, "app": {"live"}, "stream": {"camera"},
	}, true)
	if asFloat(got["code"]) == 0 || len(*calls) != 0 || len(audit.List()) != 1 {
		t.Fatalf("result=%+v calls=%+v audit=%+v", got, *calls, audit.List())
	}
}

func TestImportOnvifPullRedactsStandaloneUsernameEverywhereAndKeepsKey(t *testing.T) {
	const rawURL = "rtsp://camera-user:camera-pass@10.0.0.2/live"
	h, audit, _ := newOnvifWebRTCHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":0,"msg":"created for camera-user with camera-pass","data":{"key":"pull-key","url":"` + rawURL + `"}}`
	})
	got := h.ImportOnvifPull("node-1", "admin", url.Values{
		"url": {rawURL}, "app": {"live"}, "stream": {"camera"},
	}, true)
	if asFloat(got["code"]) != 0 || got["key"] != "pull-key" {
		t.Fatalf("result lost functional key: %+v", got)
	}
	blob, _ := json.Marshal(map[string]any{
		"result": got, "notice": asString(got["msg"]), "audit": audit.List(),
	})
	for _, leaked := range []string{"camera-user", "camera-pass", rawURL} {
		if strings.Contains(string(blob), leaked) {
			t.Fatalf("credential %q leaked: %s", leaked, blob)
		}
	}
	if !strings.Contains(string(blob), "[REDACTED]") {
		t.Fatalf("redaction marker missing: %s", blob)
	}
	entries := audit.List()
	if len(entries) != 2 || entries[0].Phase != "intent" || entries[1].Phase != "result" {
		t.Fatalf("audit pairing=%+v", entries)
	}
}

func TestImportOnvifPullShortUsernameDoesNotDamageNormalNotice(t *testing.T) {
	const notice = "camera remains active"
	h, audit, _ := newOnvifWebRTCHub(t, func(api string) string {
		if api == "listStreamProxy" {
			return `{"code":0,"data":[]}`
		}
		return `{"code":0,"msg":"` + notice + `","data":{"key":"short-user-key"}}`
	})
	got := h.ImportOnvifPull("node-1", "admin", url.Values{
		"url": {"rtsp://a:camera-pass@10.0.0.2/live"},
		"app": {"live"}, "stream": {"camera"},
	}, true)
	if asFloat(got["code"]) != 0 || got["key"] != "short-user-key" ||
		asString(got["msg"]) != notice {
		t.Fatalf("short username damaged result/notice: %+v", got)
	}
	entries := audit.List()
	if len(entries) != 2 || entries[1].Message != notice {
		t.Fatalf("short username damaged audit: %+v", entries)
	}
}
