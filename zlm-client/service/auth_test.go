package service

import (
	"testing"
	"time"
)

func TestSessionRoundTrip(t *testing.T) {
	now := time.Date(2026, 8, 21, 15, 0, 0, 0, time.UTC)
	tok, exp := IssueSession("admin", now)
	if exp.Sub(now) != 14*24*time.Hour {
		t.Fatalf("exp=%s", exp)
	}
	user, ok := ParseSession(tok, now)
	if !ok || user != "admin" {
		t.Fatalf("parse got %s %v", user, ok)
	}
	if _, ok := ParseSession(tok, exp.Add(time.Second)); ok {
		t.Fatal("expired token accepted")
	}
	if _, ok := ParseSession(tok+"x", now); ok {
		t.Fatal("tampered token accepted")
	}
	if _, ok := ParseSession("", now); ok {
		t.Fatal("empty token accepted")
	}
}

func TestCheckLogin(t *testing.T) {
	if err := CheckLogin("admin", AdminPass()); err != nil {
		t.Fatal(err)
	}
	if err := CheckLogin("admin", "wrong"); err == nil {
		t.Fatal("bad pass")
	}
	if err := CheckLogin("root", AdminPass()); err == nil {
		t.Fatal("bad user")
	}
}

func TestAuthSkip(t *testing.T) {
	if !AuthSkip("/login") || !AuthSkip("/hook/on_play") || !AuthSkip("/static/app.js") {
		t.Fatal("skip")
	}
	if AuthSkip("/") || AuthSkip("/streams") || AuthSkip("/api/history") {
		t.Fatal("must auth")
	}
}
