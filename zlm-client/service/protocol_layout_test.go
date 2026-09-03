package service

import "testing"

func TestProtocolLayoutFromConfigUsesPorts(t *testing.T) {
	got := protocolLayoutFromConfig(map[string]string{
		"rtp_proxy.port":    "10000",
		"onvif.port":        "0",
		"rtc.signalingPort": "3000",
	})
	if !got.RTP || got.ONVIF || !got.WebRTC {
		t.Fatalf("layout=%+v", got)
	}
	empty := protocolLayoutFromConfig(nil)
	if !empty.RTP || !empty.ONVIF || !empty.WebRTC {
		t.Fatalf("missing config should keep modules on: %+v", empty)
	}
	off := protocolLayoutFromConfig(map[string]string{
		"rtp_proxy.port":    "0",
		"onvif.port":        "0",
		"rtc.signalingPort": "0",
		"rtc.port":          "0",
	})
	if off.RTP || off.ONVIF || off.WebRTC {
		t.Fatalf("zero ports should hide modules: %+v", off)
	}
}

func TestFeatureCompileDisabledMatchesEnableMacrosOnly(t *testing.T) {
	for _, msg := range []string{
		"ENABLE_WEBRTC unavailable",
		"please compile with enable_rtpproxy",
		"ENABLE_ONVIF is off",
	} {
		if !FeatureCompileDisabled(msg) {
			t.Fatalf("should treat compile-disabled: %q", msg)
		}
	}
	for _, msg := range []string{
		"",
		"receiver unavailable",
		"ZLM 客户端不可用",
		"unknown node",
		"timeout",
	} {
		if FeatureCompileDisabled(msg) {
			t.Fatalf("should not treat as compile-disabled: %q", msg)
		}
	}
}

func TestProtocolReadyRequiresEnableAndCompiledAPI(t *testing.T) {
	if ProtocolReady(false) || ProtocolReady(true, "ENABLE_WEBRTC unavailable") {
		t.Fatal("disabled port or compile-macro error must not be ready")
	}
	if !ProtocolReady(true) || !ProtocolReady(true, "timeout") {
		t.Fatal("enabled module with ordinary errors should stay ready so forms remain usable")
	}
}
