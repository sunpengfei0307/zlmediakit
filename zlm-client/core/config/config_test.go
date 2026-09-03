package config

import (
	"path/filepath"
	"sync"
	"testing"
)

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
