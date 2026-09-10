//go:build !windows

package devctl

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func secureDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}
func platformCommand(a []string) (*exec.Cmd, error) { return exec.Command(a[0], a[1:]...), nil }
func setProcessGroup(c *exec.Cmd)                   { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func detach(c *exec.Cmd)                            { c.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
func ownProcess(c *exec.Cmd) (func(), func(), error) {
	pid := c.Process.Pid
	stop := func() {
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		time.Sleep(300 * time.Millisecond)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}
	return stop, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) }, nil
}
