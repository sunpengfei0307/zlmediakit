package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

func writeTOML(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFindConfigFilePrefersCwdThenCoreThenRuntime(t *testing.T) {
	t.Setenv("ZLM_ADMIN_CONFIG", "")
	root := t.TempDir()
	cwdTOML := filepath.Join(root, "config.toml")
	coreTOML := filepath.Join(root, "core", "config", "config.toml")
	writeTOML(t, cwdTOML, "listen = \":1\"\n")
	writeTOML(t, coreTOML, "listen = \":2\"\n")
	chdir(t, root)
	if got := findConfigFile(); got != cwdTOML {
		t.Fatalf("cwd config.toml should win: got %s want %s", got, cwdTOML)
	}

	if err := os.Remove(cwdTOML); err != nil {
		t.Fatal(err)
	}
	if got := findConfigFile(); got != coreTOML {
		t.Fatalf("core/config/config.toml should be second: got %s want %s", got, coreTOML)
	}

	if err := os.Remove(coreTOML); err != nil {
		t.Fatal(err)
	}
	if got := findConfigFile(); got != runtimeClientTOML {
		t.Fatalf("empty cwd should fall back to runtime: got %s want %s", got, runtimeClientTOML)
	}
}

func TestFindConfigFileWalksParentsAfterCwd(t *testing.T) {
	t.Setenv("ZLM_ADMIN_CONFIG", "")
	root := t.TempDir()
	parentTOML := filepath.Join(root, "config.toml")
	writeTOML(t, parentTOML, "listen = \":3\"\n")
	writeTOML(t, filepath.Join(root, "core", "config", "config.toml"), "listen = \":4\"\n")
	child := filepath.Join(root, "bin")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, child)
	if got := findConfigFile(); got != parentTOML {
		t.Fatalf("parent config.toml should win over parent core/config: got %s want %s", got, parentTOML)
	}
}

func TestFindConfigFileEnvOverridesSearch(t *testing.T) {
	root := t.TempDir()
	envTOML := filepath.Join(root, "from-env.toml")
	writeTOML(t, envTOML, "listen = \":9\"\n")
	writeTOML(t, filepath.Join(root, "config.toml"), "listen = \":1\"\n")
	t.Setenv("ZLM_ADMIN_CONFIG", envTOML)
	chdir(t, root)
	if got := findConfigFile(); got != envTOML {
		t.Fatalf("ZLM_ADMIN_CONFIG should win: got %s want %s", got, envTOML)
	}
}

func TestConcurrentSaveKeepsSecretsInMemoryAndSerializesFileWrites(t *testing.T) {
	oldC, oldFile := C, File
	defer func() {
		C, File = oldC, oldFile
	}()
	C = &Setup{Nodes: []Node{{ID: "zlm-1", Secret: "api-secret"}}}
	File = filepath.Join(t.TempDir(), "config.toml")

	const writers = 8
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- Save()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := C.Nodes[0].Secret; got != "api-secret" {
		t.Fatalf("secret=%q", got)
	}
	if got := New(File); len(got.Nodes) != 1 || got.Nodes[0].Secret != "" {
		t.Fatalf("persisted config leaked secret: %+v", got.Nodes)
	}
	if File == "" {
		t.Fatal("config file path was cleared")
	}
}
