package devctl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("DEVCTL_PROCESS_HELPER") != "yes" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "chunks":
		fmt.Print("before sensitive-")
		time.Sleep(20 * time.Millisecond)
		fmt.Print("secret after\n")
	case "wait":
		fmt.Println("started")
		time.Sleep(time.Minute)
	case "exit":
		os.Exit(19)
	}
	os.Exit(0)
}
func helperProcess(t *testing.T, mode string) (*Process, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "process.log")
	p, err := StartProcess([]string{os.Args[0], "-test.run=TestProcessHelper", "--", mode}, "", append(os.Environ(), "DEVCTL_PROCESS_HELPER=yes"), path, []string{"sensitive-secret"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Stop)
	return p, path
}
func TestProcessLogsRedactAcrossPipeWrites(t *testing.T) {
	p, path := helperProcess(t, "chunks")
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if err := waitProcess(ctx, p); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "[REDACTED]") || strings.Contains(string(b), "sensitive-secret") {
		t.Fatal(string(b))
	}
}
func TestProcessStopAndExitCode(t *testing.T) {
	p, _ := helperProcess(t, "wait")
	p.Stop()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("process survived stop")
	}
	p.Stop()
	p, _ = helperProcess(t, "exit")
	<-p.done
	if p.ExitCode() != 19 {
		t.Fatal(p.ExitCode())
	}
}
func TestIPCRejectsCredentialRedirectInState(t *testing.T) {
	dir := t.TempDir()
	if err := writeJSON(filepath.Join(dir, "session.json"), Session{URL: "http://127.0.0.1:80@evil.invalid", Token: "secret"}); err != nil {
		t.Fatal(err)
	}
	_, err := call(context.Background(), dir, "/status", request{})
	if err == nil || err.Error() != "invalid control endpoint" {
		t.Fatal(err)
	}
}
func TestStartupFailureIsImmediate(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "startup-error.txt"), []byte("fixture failed"), 0600)
	ctx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	err := waitSupervisor(ctx, dir)
	if err == nil || !strings.Contains(err.Error(), "fixture failed") {
		t.Fatal(err)
	}
}
func TestOversizeLogRecordIsDiscarded(t *testing.T) {
	var out strings.Builder
	redactStream(&out, strings.NewReader(strings.Repeat("x", 2*1024*1024)+"sensitive-secret\nnext\n"), []string{"sensitive-secret"})
	if out.String() != "[oversize log record omitted]\nnext\n" {
		t.Fatalf("unexpected log length %d", out.Len())
	}
}
func TestSecretServiceAccountTokenNeverCopied(t *testing.T) {
	p, r := configFixture()
	r["test/secret/creds"] = `{"type":"kubernetes.io/service-account-token","data":{"PASSWORD":"dG9rZW4="}}`
	if _, e := Prepare(context.Background(), p, r, t.TempDir()); e == nil {
		t.Fatal("service account token imported")
	}
}

func TestSupervisorStopDoesNotRequireNetwork(t *testing.T) {
	s := &supervisor{apps: map[string]*application{"order": {state: "running"}}}
	if err := s.stop("order"); err != nil {
		t.Fatal(err)
	}
	if s.apps["order"].state != "prepared" {
		t.Fatal("wrong state")
	}
}
