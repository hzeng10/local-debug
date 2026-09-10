package test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLocalLifecycleWithFakeCluster(t *testing.T) {
	if testing.Short() {
		t.Skip("builds fixture executables")
	}
	root, e := filepath.Abs("..")
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	build := func(target, path string) {
		cmd := exec.Command("go", "build", "-o", path, target)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		if b, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("build: %v %s", e, b)
		}
	}
	cli := filepath.Join(dir, "devctl"+suffix)
	fixture := filepath.Join(dir, "fixture"+suffix)
	build("./cmd/devctl", cli)
	build("./test/fixture", fixture)
	fixtureBytes, _ := os.ReadFile(fixture)
	for _, name := range []string{"kubectl", "telepresence", "java"} {
		if e := os.WriteFile(filepath.Join(dir, name+suffix), fixtureBytes, 0700); e != nil {
			t.Fatal(e)
		}
	}
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	address := l.Addr().String()
	l.Close()
	url := "http://" + address
	profile := map[string]any{
		"version": 1, "name": "order", "cluster": map[string]any{"context": "fake", "namespace": "test", "domain": "cluster.local", "kubectl": filepath.Join(dir, "kubectl"+suffix)},
		"network":      map[string]any{"backend": "telepresence", "transport": "kubectl", "managerNamespace": "existing", "gatewayNamespace": "existing", "gatewayDeployment": "dev-egress", "telepresence": filepath.Join(dir, "telepresence"+suffix)},
		"source":       map[string]any{"deployment": "order", "container": "app", "envAllow": []string{"PASSWORD", "MESSAGE"}, "overrides": map[string]string{"MESSAGE": "local-override"}},
		"application":  map[string]any{"workDir": dir, "command": []string{fixture, "app", address}, "java": filepath.Join(dir, "java"+suffix), "healthURL": url + "/health", "startupSeconds": 10, "backgroundTasksReviewed": true, "tests": map[string][]string{"smoke": {fixture, "assert", url}, "failure": {fixture, "fail", "unused"}}},
		"dependencies": []any{map[string]any{"name": "local-fixture", "url": url + "/health"}},
	}
	b, _ := json.Marshal(profile)
	profilePath := filepath.Join(dir, "profile.json")
	_ = os.WriteFile(profilePath, b, 0600)
	state := filepath.Join(dir, "state")
	run := func(command string, extra ...string) ([]byte, error) {
		a := []string{command, "--profile", profilePath, "--state-dir", state, "--format", "json"}
		a = append(a, extra...)
		cmd := exec.Command(cli, a...)
		cmd.Env = append(os.Environ(), "SPRING_APPLICATION_JSON={\"poison\":true}")
		return cmd.CombinedOutput()
	}
	defer func() { _, _ = run("disconnect") }()
	for _, command := range []string{"validate", "doctor", "connect", "run", "probe"} {
		if b, e := run(command); e != nil {
			t.Fatalf("%s: %v %s", command, e, b)
		}
	}
	if b, e := run("test", "--suite", "smoke"); e != nil {
		t.Fatalf("smoke: %v %s", e, b)
	}
	if _, e := run("test", "--suite", "failure"); e == nil {
		t.Fatal("failed test suite reported success")
	}
	if _, e := run("run"); e == nil {
		t.Fatal("duplicate JVM accepted")
	}
	b, e = run("status")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "test-secret-123") {
		t.Fatal("status leaked credential")
	}
	var status struct {
		Profiles []struct {
			LogPath string `json:"logPath"`
		}
	}
	_ = json.Unmarshal(b, &status)
	logBytes, _ := os.ReadFile(status.Profiles[0].LogPath)
	if strings.Contains(string(logBytes), "test-secret-123") {
		t.Fatal("log leaked credential")
	}
	if b, e := run("stop"); e != nil {
		t.Fatalf("stop: %v %s", e, b)
	}
	client := http.Client{Timeout: time.Second}
	if resp, e := client.Get(url + "/health"); e == nil {
		resp.Body.Close()
		t.Fatal("application survived stop")
	}
	if b, e := run("run"); e != nil {
		t.Fatalf("restart: %v %s", e, b)
	}
	if b, e := run("disconnect"); e != nil {
		t.Fatalf("disconnect: %v %s", e, b)
	}
	if _, e := os.Stat(filepath.Join(state, "network.lock")); !os.IsNotExist(e) {
		t.Fatal("network lock not cleaned")
	}
	// A failed external disconnect must not be reported as restored routing.
	if b, e := run("connect"); e != nil {
		t.Fatalf("reconnect: %v %s", e, b)
	}
	b, e = run("status")
	if e != nil {
		t.Fatal(e)
	}
	_ = json.Unmarshal(b, &status)
	marker := filepath.Join(filepath.Dir(status.Profiles[0].LogPath), "network", "fail-quit")
	_ = os.WriteFile(marker, []byte("yes"), 0600)
	b, e = run("disconnect")
	if e == nil {
		t.Fatal("cleanup failure reported success")
	}
	if !json.Valid(b) {
		t.Fatalf("disconnect must emit one JSON object: %s", b)
	}
	if _, e := os.Stat(filepath.Join(state, "cleanup-error.txt")); e != nil {
		t.Fatal("cleanup failure was not retained")
	}
	if _, e := run("connect"); e == nil {
		t.Fatal("new session ignored incomplete cleanup")
	}

}
