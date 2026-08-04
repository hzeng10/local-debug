package offline

import (
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
		out                        string
		imported, verified, parsed bool
	}{
		{"ldbg-status i=0 v=0", true, true, true},
		{"some load output\nldbg-status i=0 v=1\n", true, false, true},
		{"ldbg-status i=127 v=0", false, true, true},
		{"no marker here", false, false, false},
	}
	for _, c := range cases {
		i, v, p := parseStatus(c.out)
		if i != c.imported || v != c.verified || p != c.parsed {
			t.Errorf("parseStatus(%q) = (%v,%v,%v), want (%v,%v,%v)", c.out, i, v, p, c.imported, c.verified, c.parsed)
		}
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
