package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupGeneratedDirRecursive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".ldbg")
	files := []string{
		filepath.Join(dir, "orders.env"),
		filepath.Join(dir, "logs", "orders.log"),
		filepath.Join(dir, "logs", "deep", "b.log"),
	}
	for _, f := range files {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed := cleanupGeneratedDir(dir)
	if len(removed) != 3 {
		t.Errorf("removed = %v, want the 3 files", removed)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf(".ldbg dir must be gone, stat err = %v", err)
	}
}

func TestCleanupGeneratedDirMissing(t *testing.T) {
	if got := cleanupGeneratedDir(filepath.Join(t.TempDir(), "nope")); got != nil {
		t.Errorf("nonexistent dir must return nil, got %v", got)
	}
}
