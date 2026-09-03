package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"zlm-admin/core/config"
)

type rtpCall struct {
	api, method string
	form        url.Values
}

func newRTPHub(t *testing.T, response func(string) string) (*Hub, *recordingAudit, *[]rtpCall) {
	t.Helper()
	var mu sync.Mutex
	calls := []rtpCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		api := strings.TrimPrefix(r.URL.Path, "/index/api/")
		mu.Lock()
		calls = append(calls, rtpCall{api: api, method: r.Method, form: r.Form})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response(api)))
	}))
	t.Cleanup(srv.Close)
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL, Secret: "rtp-secret"})
	audit := &recordingAudit{}
	return &Hub{zlm: &zlmClient{http: srv.Client()}, audit: audit}, audit, &calls
}

func TestListRTPUsesExactReadAPIsAndKeepsDetailFailureLocal(t *testing.T) {
	h, _, calls := newRTPHub(t, func(api string) string {
		switch api {
		case "listRtpServer":
			return `{"code":0,"data":[{"vhost":"__defaultVhost__","app":"rtp","stream_id":"340200","port":10000,"ssrc":1,"tcp_mode":1,"only_track":0},{"vhost":"__defaultVhost__","app":"rtp","stream_id":"bad","port":10002}]}`
		case "getRtpInfo":
			return `{"code":-1,"msg":"detail unavailable"}`
		case "listRtpSender":
			return `{"code":0,"data":["1234567890"],"bytesSpeed":2048,"totalBytes":4096}`
		default:
			return `{"code":-1,"msg":"unexpected"}`
		}
	})
	view := h.ListRTP("node-1", "__defaultVhost__", "live", "camera")
	if len(view.Receivers.Items) != 2 || view.Receivers.Items[0]["_error"] == nil {
		t.Fatalf("receivers=%+v", view.Receivers)
	}
	if len(view.Senders.Items) != 1 || view.Senders.Items[0]["ssrc"] != "1234567890" ||
		asFloat(view.Senders.Items[0]["bytesSpeed"]) != 2048 {
		t.Fatalf("senders=%+v", view.Senders)
	}
	expected := map[string][]url.Values{
		"listRtpServer": {
			{"secret": {"rtp-secret"}},
		},
		"getRtpInfo": {
			{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"rtp"}, "stream_id": {"340200"}},
			{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"rtp"}, "stream_id": {"bad"}},
		},
		"listRtpSender": {
			{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"camera"}},
		},
	}
	seen := map[string]int{}
	for _, call := range *calls {
		if call.method != http.MethodGet {
			t.Fatalf("%s method=%s", call.api, call.method)
		}
		index := seen[call.api]
		wantCalls, ok := expected[call.api]
		if !ok || index >= len(wantCalls) {
			t.Fatalf("unexpected read call %+v", call)
		}
		if call.form.Encode() != wantCalls[index].Encode() {
			t.Fatalf("%s query=%v want=%v", call.api, call.form, wantCalls[index])
		}
		seen[call.api]++
	}
	for api, want := range expected {
		if seen[api] != len(want) {
			t.Fatalf("%s calls=%d want=%d all=%+v", api, seen[api], len(want), *calls)
		}
	}
}

func TestListRTPSenderStatsOnlyApplyToSingleSSRC(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		count      int
		stats      bool
	}{
		{name: "single", data: `["100"]`, count: 1, stats: true},
		{name: "multiple", data: `["100","200"]`, count: 2, stats: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _ := newRTPHub(t, func(api string) string {
				switch api {
				case "listRtpServer":
					return `{"code":0,"data":[]}`
				case "listRtpSender":
					return `{"code":0,"data":` + tc.data + `,"bytesSpeed":2048,"totalBytes":4096}`
				default:
					return `{"code":-1}`
				}
			})
			view := h.ListRTP("node-1", "tenant-vhost", "live", "camera")
			if len(view.Senders.Items) != tc.count {
				t.Fatalf("senders=%+v", view.Senders.Items)
			}
			for _, row := range view.Senders.Items {
				if tc.stats {
					if asFloat(row["bytesSpeed"]) != 2048 || asFloat(row["totalBytes"]) != 4096 || row["_stats_unavailable"] != nil {
						t.Fatalf("single sender stats=%+v", row)
					}
				} else if row["bytesSpeed"] != nil || row["totalBytes"] != nil || !asTruthy(row["_stats_unavailable"]) ||
					row["_stats_note"] != "当前ZLM API不提供逐发送器统计" {
					t.Fatalf("multiple sender stats must be unavailable: %+v", row)
				}
			}
		})
	}
}

func TestRTPReadSectionFailuresAreIndependent(t *testing.T) {
	h, _, _ := newRTPHub(t, func(api string) string {
		if api == "listRtpServer" {
			return `{"code":-1,"msg":"receiver unavailable"}`
		}
		return `{"code":0,"data":[]}`
	})
	view := h.ListRTP("node-1", "__defaultVhost__", "live", "camera")
	if !strings.Contains(view.Receivers.Error, "receiver unavailable") || view.Senders.Error != "" {
		t.Fatalf("view=%+v", view)
	}
}

func TestRTPOperationsUsePOSTExactAPIAndPairedAudit(t *testing.T) {
	cases := []struct {
		action string
		form   url.Values
		want   url.Values
		reply  string
	}{
		{RTPOpenServer,
			url.Values{"port": {"0"}, "tcp_mode": {"2"}, "stream_id": {"recv"}, "app": {"rtp"}, "local_ip": {"0.0.0.0"}, "only_track": {"2"}, "re_use_port": {"1"}, "ssrc": {"4294967295"}, "unexpected": {"leak"}},
			url.Values{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "port": {"0"}, "tcp_mode": {"2"}, "stream_id": {"recv"}, "app": {"rtp"}, "local_ip": {"0.0.0.0"}, "only_track": {"2"}, "re_use_port": {"1"}, "ssrc": {"4294967295"}}, `{"code":0,"port":10000}`},
		{RTPOpenServerMultiplex,
			url.Values{"vhost": {"tenant-vhost"}, "port": {"0"}, "tcp_mode": {"1"}, "stream_id": {"mux"}, "app": {"rtp"}, "local_ip": {"::"}, "only_track": {"1"}, "unexpected": {"leak"}},
			url.Values{"secret": {"rtp-secret"}, "vhost": {"tenant-vhost"}, "port": {"0"}, "tcp_mode": {"1"}, "stream_id": {"mux"}, "app": {"rtp"}, "local_ip": {"::"}, "only_track": {"1"}}, `{"code":0,"port":10002}`},
		{RTPConnectServer,
			url.Values{"vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "dst_url": {"192.0.2.10"}, "dst_port": {"65535"}, "unexpected": {"leak"}},
			url.Values{"secret": {"rtp-secret"}, "vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "dst_url": {"192.0.2.10"}, "dst_port": {"65535"}}, `{"code":0}`},
		{RTPCloseServer, url.Values{"vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "unexpected": {"leak"}}, url.Values{"secret": {"rtp-secret"}, "vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}}, `{"code":0,"hit":1}`},
		{RTPUpdateSSRC, url.Values{"vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "ssrc": {"0"}, "unexpected": {"leak"}}, url.Values{"secret": {"rtp-secret"}, "vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "ssrc": {"0"}}, `{"code":0}`},
		{RTPPauseCheck, url.Values{"vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "pause_seconds": {"86400"}, "unexpected": {"leak"}}, url.Values{"secret": {"rtp-secret"}, "vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "pause_seconds": {"86400"}}, `{"code":0}`},
		{RTPResumeCheck, url.Values{"vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}, "unexpected": {"leak"}}, url.Values{"secret": {"rtp-secret"}, "vhost": {"tenant-vhost"}, "stream_id": {"recv"}, "app": {"rtp"}}, `{"code":0}`},
		{RTPStartSend,
			url.Values{"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "dst_url": {"gb.example"}, "dst_port": {"1"}, "is_udp": {"1"}, "src_port": {"0"}, "pt": {"127"}, "type": {"2"}, "only_audio": {"1"}, "from_mp4": {"0"}, "ssrc_multi_send": {"1"}, "udp_rtcp_timeout": {"3600000"}, "close_delay_ms": {"3600000"}, "enable_origin_recv_limit": {"1"}, "unexpected": {"leak"}},
			url.Values{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "dst_url": {"gb.example"}, "dst_port": {"1"}, "is_udp": {"1"}, "src_port": {"0"}, "pt": {"127"}, "type": {"2"}, "only_audio": {"1"}, "from_mp4": {"0"}, "ssrc_multi_send": {"1"}, "udp_rtcp_timeout": {"3600000"}, "close_delay_ms": {"3600000"}, "enable_origin_recv_limit": {"1"}}, `{"code":0,"local_port":20000}`},
		{RTPStartSendPassive,
			url.Values{"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "ssrc": {"2"}, "src_port": {"0"}, "pt": {"96"}, "type": {"1"}, "only_audio": {"0"}, "from_mp4": {"0"}, "enable_origin_recv_limit": {"0"}, "unexpected": {"leak"}},
			url.Values{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "ssrc": {"2"}, "src_port": {"0"}, "pt": {"96"}, "type": {"1"}, "only_audio": {"0"}, "from_mp4": {"0"}, "enable_origin_recv_limit": {"0"}}, `{"code":0,"local_port":20002}`},
		{RTPStartSendTalk,
			url.Values{"vhost": {"__defaultVhost__"}, "app": {"rtp"}, "stream": {"talk"}, "ssrc": {"3"}, "recv_stream_id": {"peer"}, "pt": {"0"}, "type": {"0"}, "only_audio": {"1"}, "from_mp4": {"0"}, "enable_origin_recv_limit": {"1"}, "unexpected": {"leak"}},
			url.Values{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"rtp"}, "stream": {"talk"}, "ssrc": {"3"}, "recv_stream_id": {"peer"}, "pt": {"0"}, "type": {"0"}, "only_audio": {"1"}, "from_mp4": {"0"}, "enable_origin_recv_limit": {"1"}}, `{"code":0,"local_port":20004}`},
		{RTPStopSend, url.Values{"vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "unexpected": {"leak"}}, url.Values{"secret": {"rtp-secret"}, "vhost": {"__defaultVhost__"}, "app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}}, `{"code":0}`},
	}
	for _, tc := range cases {
		t.Run(tc.action, func(t *testing.T) {
			h, audit, calls := newRTPHub(t, func(api string) string {
				if api != tc.action {
					return `{"code":-1,"msg":"wrong api"}`
				}
				return tc.reply
			})
			got := h.RTPOperation("node-1", "admin", tc.action, tc.form)
			if asFloat(got["code"]) != 0 {
				t.Fatalf("result=%+v", got)
			}
			if len(*calls) != 1 || (*calls)[0].api != tc.action || (*calls)[0].method != http.MethodPost {
				t.Fatalf("calls=%+v", *calls)
			}
			if (*calls)[0].form.Encode() != tc.want.Encode() {
				t.Fatalf("%s form=%v want=%v", tc.action, (*calls)[0].form, tc.want)
			}
			entries := audit.List()
			if len(entries) != 2 || entries[0].Phase != "intent" || entries[1].Phase != "result" ||
				entries[0].OperationID == "" || entries[0].OperationID != entries[1].OperationID || !entries[1].Success {
				t.Fatalf("audit=%+v", entries)
			}
		})
	}
}

func TestRTPValidationRejectsUnsafeAndOutOfRangeWithoutCallingZLM(t *testing.T) {
	tests := []struct {
		action string
		form   url.Values
	}{
		{RTPOpenServer, url.Values{"port": {"-1"}, "stream_id": {"recv"}}},
		{RTPOpenServer, url.Values{"port": {"65536"}, "stream_id": {"recv"}}},
		{RTPOpenServer, url.Values{"port": {"0"}, "tcp_mode": {"3"}, "stream_id": {"recv"}}},
		{RTPOpenServerMultiplex, url.Values{"port": {"0"}, "tcp_mode": {"2"}, "stream_id": {"recv"}}},
		{RTPOpenServer, url.Values{"port": {"0"}, "stream_id": {"../recv"}}},
		{RTPOpenServer, url.Values{"port": {"0"}, "stream_id": {"bad\nid"}}},
		{RTPOpenServer, url.Values{"port": {"0"}, "stream_id": {"recv"}, "ssrc": {"4294967296"}}},
		{RTPConnectServer, url.Values{"stream_id": {"recv"}, "dst_url": {"http://host"}, "dst_port": {"10000"}}},
		{RTPConnectServer, url.Values{"stream_id": {"recv"}, "dst_url": {"user@host"}, "dst_port": {"10000"}}},
		{RTPConnectServer, url.Values{"stream_id": {"recv"}, "dst_url": {"host/path"}, "dst_port": {"10000"}}},
		{RTPConnectServer, url.Values{"stream_id": {"recv"}, "dst_url": {"host"}, "dst_port": {"0"}}},
		{RTPOpenServer, url.Values{"port": {"0"}, "stream_id": {"recv"}, "local_ip": {"host.example"}}},
		{RTPPauseCheck, url.Values{"stream_id": {"recv"}, "pause_seconds": {"86401"}}},
		{RTPStartSend, url.Values{"app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "dst_url": {"host"}, "dst_port": {"10000"}, "is_udp": {"true"}}},
		{RTPStartSend, url.Values{"app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "dst_url": {"host?q=x"}, "dst_port": {"10000"}, "is_udp": {"1"}}},
		{RTPStartSend, url.Values{"app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "dst_url": {"host"}, "dst_port": {"10000"}, "is_udp": {"1"}, "pt": {"128"}}},
		{RTPStartSendPassive, url.Values{"app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "src_port": {"65536"}}},
		{RTPStartSendTalk, url.Values{"app": {"rtp"}, "stream": {"talk"}, "ssrc": {"1"}, "recv_stream_id": {""}}},
		{RTPStopSend, url.Values{"app": {""}, "stream": {"cam"}}},
	}
	for i, tc := range tests {
		t.Run(fmt.Sprintf("%02d", i), func(t *testing.T) {
			h, audit, calls := newRTPHub(t, func(string) string { return `{"code":0}` })
			got := h.RTPOperation("node-1", "admin", tc.action, tc.form)
			if asFloat(got["code"]) == 0 || len(*calls) != 0 || len(audit.List()) != 1 || audit.List()[0].Success {
				t.Fatalf("result=%+v calls=%+v audit=%+v", got, *calls, audit.List())
			}
		})
	}
}

func TestRTPAuditPrewriteFailsClosedAndResponsesWithNoEffectFail(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"code":0,"hit":1}`))
	}))
	defer srv.Close()
	withTestNode(t, config.Node{ID: "node-1", API: srv.URL})
	h := &Hub{zlm: &zlmClient{http: srv.Client()}}
	got := h.RTPOperation("node-1", "admin", RTPCloseServer, url.Values{"stream_id": {"recv"}})
	if asFloat(got["code"]) == 0 || calls != 0 || !strings.Contains(asString(got["msg"]), "审计") {
		t.Fatalf("result=%+v calls=%d", got, calls)
	}

	for _, tc := range []struct {
		action, reply string
		form          url.Values
	}{
		{RTPCloseServer, `{"code":0,"hit":0}`, url.Values{"stream_id": {"recv"}}},
		{RTPOpenServer, `{"code":0,"port":0}`, url.Values{"port": {"0"}, "stream_id": {"recv"}}},
		{RTPStartSend, `{"code":0,"local_port":0}`, url.Values{"app": {"live"}, "stream": {"cam"}, "ssrc": {"1"}, "dst_url": {"host"}, "dst_port": {"10000"}, "is_udp": {"1"}}},
		{RTPStopSend, `{"code":-1,"msg":"stopSendRtp failed"}`, url.Values{"app": {"live"}, "stream": {"cam"}}},
	} {
		h2, audit, _ := newRTPHub(t, func(string) string { return tc.reply })
		result := h2.RTPOperation("node-1", "admin", tc.action, tc.form)
		if asFloat(result["code"]) == 0 || len(audit.List()) != 2 || audit.List()[1].Success {
			t.Fatalf("action=%s result=%+v audit=%+v", tc.action, result, audit.List())
		}
	}
}

func TestRTPTransportErrorsAndAuditTargetsDoNotLeakSecret(t *testing.T) {
	const secret = "super-secret"
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("down ?secret=%s %s", secret, r.URL.String())
	})
	withTestNode(t, config.Node{ID: "node-1", API: "http://zlm.invalid", Secret: secret})
	audit := &recordingAudit{}
	h := &Hub{zlm: &zlmClient{http: &http.Client{Transport: transport}}, audit: audit}
	got := h.RTPOperation("node-1", "admin", RTPConnectServer, url.Values{
		"stream_id": {"recv"}, "dst_url": {"host.example"}, "dst_port": {"10000"},
	})
	if strings.Contains(fmt.Sprint(got), secret) {
		t.Fatalf("result leaked secret: %+v", got)
	}
	for _, entry := range audit.List() {
		if strings.Contains(entry.Target, secret) || strings.Contains(entry.Message, secret) ||
			strings.Contains(strings.ToLower(entry.Message), "secret=") {
			t.Fatalf("audit leaked secret: %+v", entry)
		}
	}

	hResponse, responseAudit, _ := newRTPHub(t, func(string) string {
		return `{"code":-1,"msg":"authentication failed for rtp-secret"}`
	})
	responseResult := hResponse.RTPOperation("node-1", "admin", RTPCloseServer, url.Values{"stream_id": {"recv"}})
	if strings.Contains(fmt.Sprint(responseResult), "rtp-secret") {
		t.Fatalf("ZLM response leaked configured secret: %+v", responseResult)
	}
	for _, entry := range responseAudit.List() {
		if strings.Contains(entry.Message, "rtp-secret") {
			t.Fatalf("audit leaked configured secret: %+v", entry)
		}
	}

	h2, audit2, calls := newRTPHub(t, func(string) string { return `{"code":0}` })
	_ = h2.RTPOperation("node-1", "admin", RTPConnectServer, url.Values{
		"stream_id": {"recv"}, "dst_url": {"user:password@host/path?token=secret"}, "dst_port": {"10000"},
	})
	if len(*calls) != 0 || len(audit2.List()) != 1 {
		t.Fatalf("invalid target should be rejected and audited: calls=%+v audit=%+v", *calls, audit2.List())
	}
	target := audit2.List()[0].Target
	for _, secretPart := range []string{"user", "password", "token", "secret", "?"} {
		if strings.Contains(target, secretPart) {
			t.Fatalf("audit target leaked submitted credential fragment %q: %s", secretPart, target)
		}
	}
}
