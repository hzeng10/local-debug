package offline

import (
	"context"
	"strings"
	"testing"
)

// Import, verify and clean-up must travel as ONE remote command: a node then
// costs two round trips instead of four, which matters most when each one would
// otherwise ask a human for a password.
func TestRemoteScriptIsOneRoundTrip(t *testing.T) {
	s := remoteScript(`sudo docker load -i "/tmp/t.tar"`, `sudo docker image inspect "img:1"`, "/tmp/t.tar", true, false)
	for _, want := range []string{"docker load", "docker image inspect", `rm -f "/tmp/t.tar"`, "ldbg-status i=$i v=$v"} {
		if !strings.Contains(s, want) {
			t.Errorf("script %q must contain %q", s, want)
		}
	}
	// Verification must not run when the import failed — otherwise a failed load
	// followed by a stale image on the node would look like success.
	if !strings.Contains(s, "if [ $i -eq 0 ]; then") {
		t.Errorf("verify must be guarded by the import's exit code: %q", s)
	}

	// --keep-remote leaves the archive behind.
	if s := remoteScript("imp", "ver", "/tmp/t.tar", true, true); strings.Contains(s, "rm -f") {
		t.Errorf("--keep-remote must not delete the archive: %q", s)
	}
	// An unknown runtime has no verify command at all.
	if s := remoteScript("imp", "", "/tmp/t.tar", false, false); strings.Contains(s, "if [ $i -eq 0 ]") {
		t.Errorf("no verify step expected: %q", s)
	}
}

func TestParseStatus(t *testing.T) {
	cases := []struct {
		out  string
		want remoteStatus
	}{
		{"ldbg-status i=0 v=0", remoteStatus{parsed: true, imported: true, verified: true}},
		{"some load output\nldbg-status i=0 v=1\n", remoteStatus{parsed: true, imported: true}},
		{"ldbg-status i=127 v=0", remoteStatus{parsed: true, verified: true}},
		{"no marker here", remoteStatus{}},
		// The probing script's extended marker names the engine it found and
		// whether skip-present hit.
		{"ldbg-status i=0 v=0 rt=docker s=0", remoteStatus{parsed: true, imported: true, verified: true, runtime: "docker"}},
		{"ldbg-status i=0 v=0 rt=containerd s=1", remoteStatus{parsed: true, imported: true, verified: true, runtime: "containerd", skipped: true}},
		{"ldbg-status i=127 v=1 rt=none s=0", remoteStatus{parsed: true, runtime: "none"}},
	}
	for _, c := range cases {
		if got := parseStatus(c.out); got != c.want {
			t.Errorf("parseStatus(%q) = %+v, want %+v", c.out, got, c.want)
		}
	}
}

// The error tail must show the runtime's own message, not ldbg's marker line —
// the marker is always the LAST line, so without stripping it every import
// failure would read "import failed on the node: ldbg-status i=1 v=0".
func TestWithoutMarker(t *testing.T) {
	out := "open /tmp/t.tar: no such file\nldbg-status i=1 v=0"
	if got := tail(withoutMarker(out)); got != ": open /tmp/t.tar: no such file" {
		t.Errorf("tail(withoutMarker) = %q", got)
	}
}

// An unknown runtime used to be an immediate refusal. That stranded exactly the
// clusters this tool exists for: reachable over SSH, docker-only, but with a
// kubelet that reports the runtime in a nonstandard form. Now it probes.
func TestUnknownRuntimeFallsBackToProbing(t *testing.T) {
	t.Setenv(PasswordEnv, "")
	r := TransferAndImport(context.Background(),
		Target{Name: "n1", Address: "root@10.0.0.1", Runtime: RuntimeUnknown, RuntimeRaw: "weird://1.0"},
		"/tmp/t.tar", "img:1", SSHOpts{DryRun: true, Sudo: true})
	if r.Error != "" {
		t.Fatalf("unknown runtime must probe, not fail: %q", r.Error)
	}
	joined := strings.Join(r.Commands, "\n")
	for _, want := range []string{"command -v docker", "command -v isula", "rt=$rt"} {
		if !strings.Contains(joined, want) {
			t.Errorf("dry-run must show the probe script (missing %q): %q", want, joined)
		}
	}
	// A known runtime keeps the direct, non-probing script.
	r = TransferAndImport(context.Background(),
		Target{Name: "n1", Address: "root@10.0.0.1", Runtime: RuntimeDocker},
		"/tmp/t.tar", "img:1", SSHOpts{DryRun: true, Sudo: true})
	if joined := strings.Join(r.Commands, "\n"); strings.Contains(joined, "command -v docker") {
		t.Errorf("a known runtime must not probe: %q", joined)
	}
	// --import-cmd still overrides everything, probing included.
	r = TransferAndImport(context.Background(),
		Target{Name: "n1", Address: "root@10.0.0.1", Runtime: RuntimeUnknown},
		"/tmp/t.tar", "img:1", SSHOpts{DryRun: true, ImportCmd: "my-loader %s"})
	if joined := strings.Join(r.Commands, "\n"); strings.Contains(joined, "command -v") || !strings.Contains(joined, "my-loader") {
		t.Errorf("--import-cmd must win over probing: %q", joined)
	}
}

// Setting the password environment variable is the signal for "auto": it is the
// one case the system ssh binary cannot serve, because OpenSSH only reads a
// password from /dev/tty and an agent has no terminal.
func TestUseNative(t *testing.T) {
	t.Setenv(PasswordEnv, "")
	if (SSHOpts{Transport: "auto"}).UseNative() {
		t.Error("auto without a password must use the system ssh")
	}
	if !(SSHOpts{Transport: "native"}).UseNative() {
		t.Error("explicit native ignored")
	}

	t.Setenv(PasswordEnv, "hunter2")
	if !(SSHOpts{Transport: "auto"}).UseNative() {
		t.Error("auto with a password must switch to the native transport")
	}
	// An explicit choice still wins over the environment.
	if (SSHOpts{Transport: "system"}).UseNative() {
		t.Error("explicit system must not be overridden by the environment")
	}
}

// Without a terminal, ssh cannot prompt at all — BatchMode turns "hang forever"
// into an immediate error, and ConnectTimeout bounds an unreachable node.
func TestBatchOptsOnlyWhenNonInteractive(t *testing.T) {
	c := &sysConn{target: "root@10.0.0.1", opts: SSHOpts{}}
	got := strings.Join(c.sshArgs("true"), " ")
	for _, want := range []string{"BatchMode=yes", "ConnectTimeout=10"} {
		if !strings.Contains(got, want) {
			t.Errorf("non-interactive ssh args %q must contain %q", got, want)
		}
	}
	ci := &sysConn{target: "root@10.0.0.1", opts: SSHOpts{Interactive: true}}
	if strings.Contains(strings.Join(ci.sshArgs("true"), " "), "BatchMode") {
		t.Error("an interactive run must still be able to prompt")
	}
	// --ssh-opts is appended last so the user can override these defaults.
	cu := &sysConn{target: "root@10.0.0.1", opts: SSHOpts{Opts: []string{"-o", "BatchMode=no"}}}
	args := cu.sshArgs("true")
	first, last := indexOfArg(args, "BatchMode=yes"), indexOfArg(args, "BatchMode=no")
	if first < 0 || last < first {
		t.Errorf("user options must come after the defaults: %v", args)
	}
}

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func TestSplitUserHostAndPort(t *testing.T) {
	if u, h := splitUserHost("root@10.0.0.1", "admin"); u != "root" || h != "10.0.0.1" {
		t.Errorf("splitUserHost = %q, %q", u, h)
	}
	if u, h := splitUserHost("10.0.0.1", "admin"); u != "admin" || h != "10.0.0.1" {
		t.Errorf("splitUserHost = %q, %q", u, h)
	}
	if got := withPort("10.0.0.1", 0); got != "10.0.0.1:22" {
		t.Errorf("withPort default = %q", got)
	}
	if got := withPort("10.0.0.1", 2222); got != "10.0.0.1:2222" {
		t.Errorf("withPort = %q", got)
	}
	// An address that already carries a port is left alone.
	if got := withPort("10.0.0.1:2222", 22); got != "10.0.0.1:2222" {
		t.Errorf("withPort should not double up: %q", got)
	}
}
