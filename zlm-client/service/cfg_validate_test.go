package service

import "testing"

func TestValidateZLMConfig(t *testing.T) {
	issues := ValidateZLMConfig(map[string]string{
		"log.level": "2",
		"http.port": "8090",
		"protocol.enable_hls": "1",
		"hook.on_play": "http://127.0.0.1:7788/hook/on_play",
	})
	if HasFatalCfgIssue(issues) {
		t.Fatalf("valid cfg flagged: %+v", issues)
	}
	bad := ValidateZLMConfig(map[string]string{
		"log.level": "9",
		"http.port": "70000",
		"protocol.enable_hls": "yes",
		"hook.on_play": "ftp://x",
	})
	if !HasFatalCfgIssue(bad) {
		t.Fatal("expected fatals")
	}
	by := IssueByKey(bad)
	for _, k := range []string{"log.level", "http.port", "protocol.enable_hls", "hook.on_play"} {
		if by[k] == "" {
			t.Fatalf("missing issue for %s: %+v", k, by)
		}
	}
	if s := FormatCfgIssues(bad); !hasPrefix(s, "参数不合规") {
		t.Fatalf("format=%s", s)
	}
	okHook := ValidateZLMConfig(map[string]string{
		"hook.on_rtp_server_timeout": "http://127.0.0.1:7788/hook/on_rtp_server_timeout",
		"hook.timeoutSec":            "30",
	})
	if HasFatalCfgIssue(okHook) {
		t.Fatalf("hook timeout url flagged: %+v", okHook)
	}
	if CfgPlaceholder("hook.on_rtp_server_timeout") != "http(s) 地址，可留空" {
		t.Fatal(CfgPlaceholder("hook.on_rtp_server_timeout"))
	}
}

func hasPrefix(s, p string) bool {
	return len(s) >= len(p) && s[:len(p)] == p
}

func TestValidateOpsConfig(t *testing.T) {
	ok := ValidateOpsConfig(OpsConfig{
		Root: "/data/sunpf/tools/zlm-admin/zlm-server",
		API:  "http://127.0.0.1:8090",
		Base: "/data/zlm",
	})
	if HasFatalCfgIssue(ok) {
		t.Fatalf("ops fatals: %+v", ok)
	}
	bad := ValidateOpsConfig(OpsConfig{Root: "zlm-server", API: "8090", Base: "data/zlm", EnableDash: true})
	if !HasFatalCfgIssue(bad) {
		t.Fatal("expected ops fatals")
	}
	by := IssueByKey(bad)
	if by["root"] == "" || by["api"] == "" || by["base"] == "" || by["ffmpeg"] == "" {
		t.Fatalf("ops issues=%+v", by)
	}
	keepBad := ValidateOpsConfig(OpsConfig{
		Root: "/data/zlm", API: "http://127.0.0.1:8090", Base: "/data/zlm",
		CheckLiveKeep: true, LiveKeepRaw: "10",
	})
	if IssueByKey(keepBad)["live_keep_sec"] == "" {
		t.Fatalf("live keep should fail: %+v", keepBad)
	}
}

func TestCfgPlaceholder(t *testing.T) {
	if CfgPlaceholder("http.port") != "有效范围 0-65535" {
		t.Fatal(CfgPlaceholder("http.port"))
	}
	if CfgPlaceholder("protocol.enable_hls") != "有效范围 0 或 1" {
		t.Fatal(CfgPlaceholder("protocol.enable_hls"))
	}
	if CfgPlaceholder("hls.deleteDelaySec") != "有效范围 0-86400 秒" {
		t.Fatal(CfgPlaceholder("hls.deleteDelaySec"))
	}
}
