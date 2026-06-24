package springconfig

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteIntelliJRunConfig(t *testing.T) {
	dir := t.TempDir()
	// gradle (default when no build file)
	path, note, err := WriteIntelliJRunConfig(dir, "orders", ".ldbg/orders.env", "")
	if err != nil {
		t.Fatalf("intellij: %v", err)
	}
	if !strings.Contains(note, "envfile") && !strings.Contains(note, "EnvFile") {
		t.Errorf("note should mention the EnvFile plugin, got %q", note)
	}
	b, _ := os.ReadFile(path)
	s := string(b)
	if !strings.Contains(s, "GradleRunConfiguration") || !strings.Contains(s, "bootRun") {
		t.Errorf("expected gradle bootRun config:\n%s", s)
	}
	if !strings.Contains(s, "net.ashald.envfile") || !strings.Contains(s, ".ldbg/orders.env") {
		t.Errorf("expected EnvFile extension pointing at the env-file:\n%s", s)
	}

	// maven (detected from pom.xml)
	mdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(mdir, "pom.xml"), []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	mpath, _, err := WriteIntelliJRunConfig(mdir, "orders", ".ldbg/orders.env", "")
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := os.ReadFile(mpath)
	if !strings.Contains(string(mb), "MavenRunConfiguration") || !strings.Contains(string(mb), "spring-boot:run") {
		t.Errorf("expected maven spring-boot:run config:\n%s", mb)
	}
}

func TestWriteVSCodeLaunch(t *testing.T) {
	dir := t.TempDir()
	path, snippet, err := WriteVSCodeLaunch(dir, "orders", ".ldbg/orders.env")
	if err != nil {
		t.Fatalf("vscode: %v", err)
	}
	if snippet {
		t.Errorf("first write should create launch.json, not a snippet")
	}
	b, _ := os.ReadFile(path)
	var doc map[string]interface{}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("launch.json is not valid JSON: %v\n%s", err, b)
	}

	// second call must not clobber; returns a snippet
	path2, snippet2, err := WriteVSCodeLaunch(dir, "orders", ".ldbg/orders.env")
	if err != nil {
		t.Fatal(err)
	}
	if !snippet2 {
		t.Errorf("second write should produce a snippet, got %s", path2)
	}
}
