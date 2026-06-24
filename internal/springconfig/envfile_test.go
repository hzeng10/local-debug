package springconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hzeng10/local-debug/internal/k8s"
)

func TestWriteEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "orders.env")
	vars := []k8s.EnvVar{
		{Name: "SPRING_PROFILES_ACTIVE", Value: "cluster", Source: "literal"},
		{Name: "DB_PASSWORD", Value: "p@ss", Secret: true, Source: "secret:db/PASSWORD"},
		{Name: "MULTILINE", Value: "a\nb", Source: "literal"},
		{Name: "POD_IP", Skipped: true, Reason: "fieldRef status.podIP is resolved at pod runtime"},
	}

	written, skipped, err := WriteEnvFile(path, vars)
	if err != nil {
		t.Fatalf("WriteEnvFile: %v", err)
	}
	if written != 3 || skipped != 1 {
		t.Fatalf("counts: written=%d skipped=%d, want 3/1", written, skipped)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	s := string(b)
	if !strings.Contains(s, "SPRING_PROFILES_ACTIVE=cluster") {
		t.Errorf("missing literal var:\n%s", s)
	}
	if !strings.Contains(s, "MULTILINE=a\\nb") {
		t.Errorf("multiline value not escaped to single line:\n%s", s)
	}
	if !strings.Contains(s, "# POD_IP skipped:") {
		t.Errorf("skipped var not commented:\n%s", s)
	}

	// File mode must be 0600 (may contain secrets).
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("env-file perm = %o, want 600", perm)
	}
}

func TestSummarizeMasksSecrets(t *testing.T) {
	mask := func(string) string { return "MASKED" }
	vars := []k8s.EnvVar{{Name: "DB_PASSWORD", Value: "p@ssword", Secret: true, Source: "secret:db/PASSWORD"}}
	lines := Summarize(vars, mask)
	if len(lines) != 1 || strings.Contains(lines[0], "p@ssword") || !strings.Contains(lines[0], "MASKED") {
		t.Errorf("secret not masked in summary: %v", lines)
	}
}
