package devctl

import (
	"context"
	"devctl.local/devctl/internal/provision"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var Version = "0.2.0"

const usage = `devctl: Windows local JVM -> shared Kubernetes dependencies

Usage: devctl COMMAND --profile FILE [--transport kubectl|ssh] [--ssh-host ALIAS]
Commands:
  doctor       Check local prerequisites and authorized Kubernetes reads
  connect      Start a managed shared network session; prepare local configuration
  run          Start a local process and wait for health readiness
  stop         Stop one local application (preserves the shared connection)
  test         Run --suite NAME using the prepared application environment
  probe        Check shared dependency DNS/TCP/HTTP connectivity
  status       Show network and local process status
  disconnect   Stop ALL managed applications and the shared network session
  validate     Validate a profile without contacting Kubernetes
  bundle       Prepare/verify offline administrator bundles
  admin        Discover/plan/apply/verify/export shared components
  version      Print the build version

Options: --state-dir DIR --format text|json --suite NAME
Profiles are JSON. See examples/*.json and README.md.
No developer command creates namespaces, deploys workloads, or intercepts traffic.
`

func Main(args []string, out, errOut io.Writer) int {
	if len(args) > 0 && (args[0] == "admin" || args[0] == "bundle") {
		return provision.Main(args, out, errOut)
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(out, usage)
		return 0
	}
	if args[0] == "version" {
		fmt.Fprintln(out, Version)
		return 0
	}
	command := args[0]
	fs := flag.NewFlagSet("devctl "+command, flag.ContinueOnError)
	fs.SetOutput(errOut)
	profile := fs.String("profile", "", "path to profile JSON")
	transport := fs.String("transport", "", "kubectl or ssh")
	host := fs.String("ssh-host", "", "SSH alias")
	root := fs.String("state-dir", defaultState(), "private local state")
	format := fs.String("format", "text", "text or json")
	suite := fs.String("suite", "smoke", "test suite")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 || (*format != "text" && *format != "json") {
		fmt.Fprintln(errOut, "invalid arguments")
		return 2
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	*root = abs
	emit := func(v any) {
		if *format == "json" {
			_ = json.NewEncoder(out).Encode(v)
		} else {
			b, _ := json.MarshalIndent(v, "", "  ")
			fmt.Fprintln(out, string(b))
		}
	}
	fail := func(err error) int {
		if *format == "json" {
			emit(map[string]any{"ok": false, "error": err.Error()})
		} else {
			fmt.Fprintln(errOut, err)
		}
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if command == "status" || command == "disconnect" {
		b, e := call(ctx, *root, "/"+command, request{})
		if e != nil {
			return fail(e)
		}
		if command == "disconnect" {
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				if _, e := os.Stat(filepath.Join(*root, "network.lock")); os.IsNotExist(e) {
					if b, e := os.ReadFile(filepath.Join(*root, "cleanup-error.txt")); e == nil {
						return fail(fmt.Errorf("%s", b))
					}
					emit(map[string]bool{"disconnected": true})
					return 0
				}
				time.Sleep(200 * time.Millisecond)
			}
			return fail(fmt.Errorf("cleanup has not finished; inspect supervisor log"))
		}
		emit(json.RawMessage(b))
		return 0
	}
	if *profile == "" {
		return fail(fmt.Errorf("--profile FILE is required"))
	}
	p, err := LoadProfile(*profile, *transport, *host)
	if err != nil {
		return fail(err)
	}
	switch command {
	case "validate":
		emit(map[string]any{"ok": true, "profile": p.Name, "backend": p.Network.Backend, "transport": p.Network.Transport})
		return 0
	case "doctor":
		checks := Doctor(ctx, p)
		emit(checks)
		for _, c := range checks {
			if !c.OK {
				return 1
			}
		}
		return 0
	case "__serve":
		if err := Serve(p, *root); err != nil {
			_ = writePrivate(filepath.Join(*root, "startup-error.txt"), []byte(err.Error()))
			return 1
		}
		return 0
	case "connect":
		if _, e := call(ctx, *root, "/status", request{}); e != nil {
			if err = startSupervisor(p, *root); err != nil {
				return fail(err)
			}
			ready, c := context.WithTimeout(ctx, 150*time.Second)
			err = waitSupervisor(ready, *root)
			c()
			if err != nil {
				return fail(err)
			}
		}
		b, e := call(ctx, *root, "/prepare", request{Profile: p.Path, Transport: p.Network.Transport, SSHHost: p.Cluster.SSHHost})
		if e != nil {
			return fail(e)
		}
		emit(json.RawMessage(b))
		return 0
	case "run", "stop", "test", "probe":
		b, e := call(ctx, *root, "/"+command, request{Name: p.Name, Suite: *suite})
		if e != nil {
			if len(b) > 0 {
				emit(json.RawMessage(b))
			} else {
				return fail(e)
			}
			return 1
		}
		emit(json.RawMessage(b))
		if command == "probe" {
			var checks []Check
			_ = json.Unmarshal(b, &checks)
			for _, c := range checks {
				if !c.OK {
					return 1
				}
			}
		}
		return 0
	default:
		return fail(fmt.Errorf("unknown command %q", command))
	}
}
func startSupervisor(p Profile, root string) error {
	if err := secureDir(root); err != nil {
		return err
	}
	if _, e := os.Stat(filepath.Join(root, "network.lock")); e == nil {
		return fmt.Errorf("network.lock exists but supervisor is unreachable; inspect existing processes before manual recovery")
	}
	if b, e := os.ReadFile(filepath.Join(root, "cleanup-error.txt")); e == nil {
		return fmt.Errorf("previous cleanup was incomplete: %s; resolve it and remove cleanup-error.txt", b)
	}
	_ = os.Remove(filepath.Join(root, "startup-error.txt"))
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"__serve", "--profile", p.Path, "--state-dir", root, "--transport", p.Network.Transport}
	if p.Cluster.SSHHost != "" {
		args = append(args, "--ssh-host", p.Cluster.SSHHost)
	}
	cmd := exec.Command(exe, args...)
	detach(cmd)
	f, err := os.OpenFile(filepath.Join(root, "supervisor.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd.Stdout = f
	cmd.Stderr = f
	if err = cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
func netURLAddress(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return "", e
	}
	port := u.Port()
	if port == "" {
		port = "80"
	}
	return net.JoinHostPort(u.Hostname(), port), nil
}

func waitSupervisor(ctx context.Context, root string) error {
	for {
		if _, err := call(ctx, root, "/status", request{}); err == nil {
			return nil
		}
		if b, err := os.ReadFile(filepath.Join(root, "startup-error.txt")); err == nil {
			return fmt.Errorf("startup failed: %s", strings.TrimSpace(string(b)))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("supervisor startup timed out; inspect private state and use disconnect before retrying")
		case <-time.After(300 * time.Millisecond):
		}
	}
}
