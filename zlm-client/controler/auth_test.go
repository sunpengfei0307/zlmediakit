package controler

import "testing"

func TestSafeNext(t *testing.T) {
	if got := safeNext("/streams"); got != "/streams" {
		t.Fatalf("got %s", got)
	}
	if got := safeNext(""); got != "/" {
		t.Fatalf("empty %s", got)
	}
	if got := safeNext("https://evil.example/x"); got != "/" {
		t.Fatalf("abs %s", got)
	}
	if got := safeNext("//evil"); got != "/" {
		t.Fatalf("proto %s", got)
	}
}
