package service

import (
	"path/filepath"
	"testing"
)

func TestNormalizePeerIP(t *testing.T) {
	got, err := normalizePeerIP("10.62.89.161:35734")
	if err != nil || got != "10.62.89.161" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	got, err = normalizePeerIP("[::1]:1935")
	if err != nil || got != "::1" {
		t.Fatalf("v6=%q err=%v", got, err)
	}
	if _, err = normalizePeerIP("not-an-ip"); err == nil {
		t.Fatal("invalid ip")
	}
}

func TestIPRuleMatchesCIDR(t *testing.T) {
	if !ipRuleMatches("10.62.0.0/16", "10.62.89.161") {
		t.Fatal("cidr should match")
	}
	if ipRuleMatches("10.62.0.0/16", "10.63.0.1") {
		t.Fatal("cidr should miss")
	}
}

func TestIPAllowModeBlacklist(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if _, err := h.AddStreamIPRule("10.62.89.161", "black", true, false, "push"); err != nil {
		t.Fatal(err)
	}
	if mode, _ := h.StreamAuthView()["ip_mode"].(string); mode != ipModeAllow {
		t.Fatalf("adding blacklist should switch to allow mode, got %q", mode)
	}
	deny, msg := h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "10.62.89.161",
	})
	if !deny || msg != "IP 在推流黑名单" {
		t.Fatalf("deny=%v msg=%q", deny, msg)
	}
	deny, _ = h.denyStreamHook("on_play", map[string]any{
		"app": "live", "stream": "cam", "ip": "10.62.89.161",
	})
	if deny {
		t.Fatal("play must still pass when only push is blacklisted")
	}
	deny, _ = h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "10.0.0.2",
	})
	if deny {
		t.Fatal("other ip must pass in allow mode")
	}
}

func TestIPDenyModeWhitelist(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if _, err := h.AddStreamIPRule("10.1.1.8", "white", true, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.SetStreamIPMode(ipModeDeny); err != nil {
		t.Fatal(err)
	}
	deny, msg := h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "10.2.2.2",
	})
	if !deny || msg != "IP 未在推流白名单" {
		t.Fatalf("deny=%v msg=%q", deny, msg)
	}
	deny, _ = h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "10.1.1.8",
	})
	if deny {
		t.Fatal("whitelisted ip must publish")
	}
}

func TestIPRestrictionRunsBeforeToken(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if err := h.SetStreamAuthEnabled(true); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddStreamAuthToken("cam", "secret-token", true, true, "live", "cam", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddStreamIPRule("8.8.8.8", "black", true, true, ""); err != nil {
		t.Fatal(err)
	}
	deny, msg := h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "8.8.8.8", "params": "token=secret-token",
	})
	if !deny || msg != "IP 在推流黑名单" {
		t.Fatalf("ip must reject before token, deny=%v msg=%q", deny, msg)
	}
	deny, _ = h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "1.1.1.1",
	})
	if !deny {
		t.Fatal("after ip pass, missing token must still reject")
	}
}

func TestAddStreamIPRuleRejectsDuplicate(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if _, err := h.AddStreamIPRule("10.1.1.1", "black", true, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.AddStreamIPRule("10.1.1.1", "black", true, false, ""); err == nil {
		t.Fatal("duplicate add must fail")
	}
}

func TestDisabledIPRuleIsIgnored(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	item, err := h.AddStreamIPRule("9.9.9.9", "black", true, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.ToggleStreamIPRule(item.ID, false); err != nil {
		t.Fatal(err)
	}
	deny, _ := h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "9.9.9.9",
	})
	if deny {
		t.Fatal("disabled blacklist must not reject")
	}
	if err := h.DeleteStreamIPRule(item.ID); err != nil {
		t.Fatal(err)
	}
}

func TestIPModeOffIgnoresLists(t *testing.T) {
	kv, err := OpenLocalKV(filepath.Join(t.TempDir(), "zlm-admin.kv"))
	if err != nil {
		t.Fatal(err)
	}
	defer kv.Close()
	h := &Hub{kv: kv}
	if _, err := h.AddStreamIPRule("8.8.8.8", "black", true, true, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.SetStreamIPMode(ipModeOff); err != nil {
		t.Fatal(err)
	}
	deny, _ := h.denyStreamHook("on_publish", map[string]any{
		"app": "live", "stream": "cam", "ip": "8.8.8.8",
	})
	if deny {
		t.Fatal("off mode must ignore blacklist")
	}
}
