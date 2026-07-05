package locallog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hzeng10/local-debug/internal/k8s"
)

func TestPathFor(t *testing.T) {
	p, err := PathFor("demo/or ders:x")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(p) {
		t.Errorf("must be absolute: %q", p)
	}
	want := filepath.Join(".ldbg", "logs", "demo-or_ders-x.log")
	if !strings.HasSuffix(p, want) {
		t.Errorf("path %q should end with %q", p, want)
	}
}

func TestOpenAppendAppendsAndCreatesParents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deep", "nested", "svc.log")
	for _, chunk := range []string{"first\n", "second\n"} {
		f, err := OpenAppend(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString(chunk); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "first\nsecond\n" {
		t.Errorf("append semantics broken: %q", b)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestStripVar(t *testing.T) {
	env := []string{"A=1", "LOGGING_FILE_NAME=/ldbg/path.log", "LOGGING_FILE_NAME=/container/app.log", "B=x=y"}
	got := StripVar(append([]string(nil), env...), "LOGGING_FILE_NAME", "/ldbg/path.log")
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %v", got)
	}
	joined := strings.Join(got, ";")
	if strings.Contains(joined, "/ldbg/path.log") {
		t.Error("ldbg-valued entry must be stripped")
	}
	if !strings.Contains(joined, "/container/app.log") {
		t.Error("same key with a different (cluster) value must be kept")
	}
	if !strings.Contains(joined, "B=x=y") {
		t.Error("values containing '=' must be preserved")
	}
}

func TestInjectEnvVarFresh(t *testing.T) {
	vars, path, err := InjectEnvVar([]k8s.EnvVar{{Name: "A", Value: "1"}}, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || !filepath.IsAbs(path) {
		t.Fatalf("path = %q", path)
	}
	last := vars[len(vars)-1]
	if last.Name != EnvVar || last.Value != path || last.Source != k8s.SourceSynthetic {
		t.Errorf("injected var wrong: %+v", last)
	}
}

func TestInjectEnvVarClusterWins(t *testing.T) {
	in := []k8s.EnvVar{{Name: EnvVar, Value: "/var/log/app.log", Source: "literal"}}
	vars, path, err := InjectEnvVar(in, "orders")
	if err != nil {
		t.Fatal(err)
	}
	if path != "" || len(vars) != 1 {
		t.Errorf("cluster-defined var must win: path=%q vars=%v", path, vars)
	}
}
