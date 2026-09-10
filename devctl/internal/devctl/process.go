package devctl

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type Process struct {
	cmd  *exec.Cmd
	done chan struct{}
	mu   sync.Mutex
	err  error
	stop func()
	once sync.Once
}

func (p *Process) Stop() {
	if p.Running() {
		p.once.Do(func() { p.stop() })
	}
}
func (p *Process) Running() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}
func (p *Process) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd.ProcessState == nil {
		return -1
	}
	return p.cmd.ProcessState.ExitCode()
}
func StartProcess(argv []string, dir string, env []string, logPath string, secrets []string) (*Process, error) {
	if len(argv) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	cmd, err := platformCommand(argv)
	if err != nil {
		return nil, err
	}
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	setProcessGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	r, w := io.Pipe()
	cmd.Stdout = w
	cmd.Stderr = w
	if err = cmd.Start(); err != nil {
		f.Close()
		r.Close()
		w.Close()
		return nil, fmt.Errorf("cannot start %s: %w", argv[0], err)
	}
	stop, cleanup, err := ownProcess(cmd)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		f.Close()
		r.Close()
		w.Close()
		return nil, err
	}
	p := &Process{cmd: cmd, done: make(chan struct{}), stop: stop}
	logDone := make(chan struct{})
	go func() { defer close(logDone); defer f.Close(); defer r.Close(); redactStream(f, r, secrets) }()
	go func() {
		err := cmd.Wait()
		cleanup()
		_ = w.Close()
		<-logDone
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
	}()
	return p, nil
}
func redactStream(dst io.Writer, src io.Reader, secrets []string) {
	// Buffer a complete line so a credential split across writes cannot evade redaction.
	// Oversize log records are discarded, never emitted as unredacted fragments.
	secrets = append([]string{}, secrets...)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	r := bufio.NewReaderSize(src, 64*1024)
	var record strings.Builder
	discard := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !discard {
			if record.Len()+len(chunk) > 1024*1024 {
				discard = true
				record.Reset()
			} else {
				record.Write(chunk)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		line := record.String()
		record.Reset()
		if discard {
			line = "[oversize log record omitted]\n"
			discard = false
		}
		for _, s := range secrets {
			if s != "" {
				for _, part := range strings.Split(s, "\n") {
					if part != "" {
						line = strings.ReplaceAll(line, part, "[REDACTED]")
					}
				}
			}
		}
		if line != "" {
			_, _ = io.WriteString(dst, line)
		}
		if err != nil {
			return
		}
	}
}
func waitProcess(ctx context.Context, p *Process) error {
	select {
	case <-ctx.Done():
		p.Stop()
		<-p.done
		return ctx.Err()
	case <-p.done:
		if p.ExitCode() != 0 {
			return fmt.Errorf("process exited with code %d", p.ExitCode())
		}
		return nil
	}
}
func runQuiet(ctx context.Context, argv []string) ([]byte, error) {
	cmd, err := platformCommand(argv)
	if err != nil {
		return nil, err
	}
	// Only used for bounded tool commands, not long-running JVMs or tunnels.
	cmd.Cancel = nil
	cmd.WaitDelay = 2 * time.Second
	var out strings.Builder
	cmd.Stdout = &out
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("cannot start %s", argv[0])
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		<-done
		return nil, ctx.Err()
	case err = <-done:
		if err != nil {
			return nil, fmt.Errorf("%s command failed", argv[0])
		}
		return []byte(out.String()), nil
	}
}
func retryUntil(ctx context.Context, delay time.Duration, fn func() error) error {
	var last error
	for {
		if last = fn(); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out: %w", last)
		case <-time.After(delay):
		}
	}
}
