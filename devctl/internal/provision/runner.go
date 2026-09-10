package provision

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Runner interface {
	Run(context.Context, []string, []byte, []string) ([]byte, error)
}
type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, a []string, in []byte, env []string) ([]byte, error) {
	if len(a) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd := exec.CommandContext(ctx, a[0], a[1:]...)
	cmd.Stdin = bytes.NewReader(in)
	cmd.Env = append(os.Environ(), env...)
	cmd.WaitDelay = 2 * time.Second
	b, e := cmd.Output()
	if e != nil {
		return b, fmt.Errorf("%s failed (%v); command output withheld because it may contain credentials", filepath.Base(a[0]), e)
	}
	return b, nil
}
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func remote(a []string) string {
	q := make([]string, len(a))
	for i, s := range a {
		q[i] = quote(s)
	}
	return "exec " + strings.Join(q, " ")
}
func sshArgs(s SSH) []string {
	a := []string{"ssh", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15"}
	if s.Port != 0 {
		a = append(a, "-p", strconv.Itoa(s.Port))
	}
	if s.IdentityFile != "" {
		a = append(a, "-i", s.IdentityFile)
	}
	if s.KnownHostsFile != "" {
		a = append(a, "-o", "UserKnownHostsFile="+s.KnownHostsFile)
	}
	if s.ProxyJump != "" {
		a = append(a, "-J", s.ProxyJump)
	}
	return a
}
func runSSH(ctx context.Context, r Runner, s SSH, a []string, in []byte) ([]byte, error) {
	if e := s.validate(); e != nil {
		return nil, e
	}
	if s.Sudo {
		a = append([]string{"sudo", "-n", "--"}, a...)
	}
	return r.Run(ctx, append(sshArgs(s), "--", s.Host, remote(a)), in, nil)
}
func secureDir(p string) error {
	if e := os.MkdirAll(p, 0700); e != nil {
		return e
	}
	if runtime.GOOS != "windows" {
		return os.Chmod(p, 0700)
	}
	b, e := exec.Command("whoami", "/user", "/fo", "csv", "/nh").Output()
	if e != nil {
		return e
	}
	rows, e := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if e != nil || len(rows) != 1 || len(rows[0]) < 2 {
		return fmt.Errorf("cannot resolve Windows SID")
	}
	sid := rows[0][1]
	if !strings.HasPrefix(sid, "S-1-") {
		return fmt.Errorf("invalid Windows SID")
	}
	if e = exec.Command("icacls", p, "/inheritance:r", "/grant:r", "*"+sid+":(OI)(CI)F").Run(); e != nil {
		return fmt.Errorf("cannot restrict Windows ACL: %w", e)
	}
	return nil
}
