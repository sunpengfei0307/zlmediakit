package controler

import (
	"os"
	"strings"
	"testing"
)

func TestControlScriptCoversTargetsAndSystemd(t *testing.T) {
	raw, err := os.ReadFile("../control.sh")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	for _, want := range []string{
		"zlm-server", "zlm-client", "zlm",
		"start", "stop", "restart", "reload",
		"systemctl", "Restart=always",
		"setServerConfig", "flock",
		"/data/zlm/bin", "/data/zlm/cfg",
		"zlm-server.ini", "zlm-client.toml",
		"sudo -n", "timeout",
		"publish_runtime",
		"先操作 server 再操作 client",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("control.sh missing %q", want)
		}
	}
	if !strings.Contains(out, `pkill -f '(^|[[:space:]/])snapd\.sh([[:space:]]|$)'`) {
		t.Fatal("control.sh must still retire leftover snapd.sh")
	}
	if strings.Contains(out, "/data/zlm/bin/MediaServer") || strings.Contains(out, "/data/zlm/bin/zlm-admin") {
		t.Fatal("runtime binaries must be zlm-server / zlm-client, not MediaServer / zlm-admin")
	}
}

func TestControlSystemdUnitsRestartAlways(t *testing.T) {
	server, err := os.ReadFile("../deploy/systemd/zlm-server.service.in")
	if err != nil {
		t.Fatal(err)
	}
	client, err := os.ReadFile("../deploy/systemd/zlm-client.service.in")
	if err != nil {
		t.Fatal(err)
	}
	ss, cs := string(server), string(client)
	for _, want := range []string{"Restart=always", "TimeoutStartSec=", "@BIN@", "@CFG@", "@DATA@"} {
		if !strings.Contains(ss, want) {
			t.Fatalf("zlm-server unit missing %q", want)
		}
		if !strings.Contains(cs, want) {
			t.Fatalf("zlm-client unit missing %q", want)
		}
	}
	if !strings.Contains(ss, "@BIN@/zlm-server") || !strings.Contains(ss, "@CFG@/zlm-server.ini") {
		t.Fatal("zlm-server unit must exec /data/zlm/bin/zlm-server with cfg ini")
	}
	if strings.Contains(ss, "MediaServer") {
		t.Fatal("zlm-server unit must not exec MediaServer by name")
	}
	if !strings.Contains(cs, "@BIN@/zlm-client") || !strings.Contains(cs, "@CFG@/zlm-client.toml") {
		t.Fatal("zlm-client unit must exec /data/zlm/bin/zlm-client with cfg toml")
	}
	if strings.Contains(cs, "zlm-admin") {
		t.Fatal("zlm-client unit must not exec zlm-admin by name")
	}
	if strings.Contains(ss, "network-online.target") || strings.Contains(cs, "network-online.target") {
		t.Fatal("units must not wait on network-online (causes start hang)")
	}
}

func TestStartScriptDelegatesToControl(t *testing.T) {
	raw, err := os.ReadFile("../start.sh")
	if err != nil {
		t.Fatal(err)
	}
	out := string(raw)
	if !strings.Contains(out, "control.sh") || !strings.Contains(out, "zlm") {
		t.Fatal("start.sh must delegate to control.sh")
	}
}
