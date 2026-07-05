package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/locallog"
	"github.com/hzeng10/local-debug/internal/mesh"
	"github.com/hzeng10/local-debug/internal/springconfig"
	"github.com/hzeng10/local-debug/internal/tp"
	"github.com/spf13/cobra"
)

var (
	upPort        string
	upRun         []string
	upEnvOut      string
	upNoMount     bool
	upKeepAmbient bool
	upRunConfig   string
	upNoLocalLog  bool
)

// upResult is the --json payload for `ldbg up`.
type upResult struct {
	Target               string                  `json:"target"`
	Namespace            string                  `json:"namespace"`
	EnvFile              string                  `json:"envFile"`
	VarsWritten          int                     `json:"varsWritten"`
	LocalLogFile         string                  `json:"localLogFile,omitempty"`
	Port                 string                  `json:"port"`
	LocalPort            int                     `json:"localPort"`
	Connected            bool                    `json:"connected"`
	InterceptActive      bool                    `json:"interceptActive"`
	Launched             bool                    `json:"launched"`
	Ambient              *mesh.AmbientAssessment `json:"ambient,omitempty"`
	AmbientOptOutApplied bool                    `json:"ambientOptOutApplied"`
}

var upCmd = &cobra.Command{
	Use:   "up <service>",
	Short: "Sync cluster env, connect, and globally intercept a service so it runs on your laptop",
	Long: `up performs the full bring-up: resolve the target workload, sync its cluster env to a
local env-file, ensure telepresence is connected, then a global (TCP) intercept that
routes the service's real cluster traffic to your local process.

Run your Spring Boot app on the intercept's local port (IDE debug, bootRun, or
java -jar) — or pass --run to have ldbg launch it for you with the synced env.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		target := args[0]

		cl, err := newK8sClient()
		if err != nil {
			return out.Failf("up", "check --kubeconfig/--context", err)
		}
		ns, err := resolveNamespace(cl)
		if err != nil {
			return out.Failf("up", "", err)
		}

		// 1) Config sync.
		sync, wl, err := syncEnvToFile(ctx, cl, ns, target, upEnvOut, !upNoLocalLog)
		if err != nil {
			return out.Failf("up", "is the service/workload name and namespace correct?", err)
		}
		out.Info("✓ synced %d env vars → %s", sync.Written, sync.EnvFile)

		// 2) Port mapping (flag wins, else derive from the Service).
		port := upPort
		if port == "" {
			port = derivePort(wl)
		}
		if port == "" {
			return out.Failf("up", "pass --port <local>:<remote>",
				fmt.Errorf("could not derive a port for %q; no Service port found", target))
		}

		// 3) Ensure telepresence is connected.
		tpc := newTPClient()
		if !tpc.Available() {
			return out.Failf("up", "install telepresence or pass --telepresence-bin", errTelepresenceMissing)
		}
		st, _ := tpc.Status(ctx)
		connected := st != nil && st.Connected
		if !connected {
			out.Info("… connecting to cluster (telepresence connect)")
			// Scope the connection to the target namespace so `down` can uninstall the
			// agent (telepresence uninstall resolves in the connected namespace).
			if cerr := tpc.Connect(ctx, tp.ConnectOpts{Namespace: ns, Context: flagContext, ManagerNamespace: managerNamespace}); cerr != nil {
				return out.Failf("up",
					"the root network daemon needs elevation once — run 'telepresence connect' yourself (sudo on Linux, admin on Windows), then re-run 'ldbg up'",
					cerr)
			}
			connected = true
		}
		out.Info("✓ connected")

		res := upResult{
			Target: target, Namespace: ns, EnvFile: sync.EnvFile, VarsWritten: sync.Written,
			LocalLogFile: sync.LocalLogFile,
			Port:         port, LocalPort: localPortOf(port), Connected: connected, InterceptActive: true,
		}

		// 4) Ambient handling. An intercepted ambient workload gets its port black-holed
		// by the istio-cni/traffic-agent conflict; exclude it from ambient first.
		nsMode, _ := cl.NamespaceDataplaneMode(ctx, ns)
		assessment := mesh.AssessWorkload(nsMode, wl.PodTemplateDataplaneMode())
		res.Ambient = &assessment
		if assessment.NeedsOptOut && !upKeepAmbient {
			out.Info("… ambient: excluding %q from ztunnel redirection (istio.io/dataplane-mode=none) so the intercept isn't black-holed", target)
			if perr := cl.SetAmbientOptOut(ctx, wl); perr != nil {
				return out.Failf("up", "needs RBAC to patch the workload, or pass --keep-ambient to skip", perr)
			}
			if werr := cl.WaitWorkloadAvailable(ctx, wl, 120*time.Second); werr != nil {
				return out.Failf("up", "the opt-out rollout did not become ready", werr)
			}
			res.AmbientOptOutApplied = true
			out.Info("✓ ambient opt-out applied (reverted by 'ldbg down')")
		} else if assessment.AlreadyOptedOut {
			out.Info("✓ ambient: %q already excluded from ambient — ok", target)
		} else if assessment.NeedsOptOut && upKeepAmbient {
			out.Info("! ambient: %q stays in ambient (--keep-ambient); in-cluster callers may see connection resets", target)
		}

		// 5) Global intercept (full takeover).
		mount := "false"
		if !upNoMount {
			mount = "false" // default off; file-mounted secrets handled in a later phase
		}
		if ierr := tpc.Intercept(ctx, tp.InterceptOpts{
			Name: target, Namespace: ns, Port: port, EnvFile: sync.EnvFile, Mount: mount,
		}); ierr != nil {
			return out.Failf("up", "is the namespace correct? is another intercept already active?", ierr)
		}
		out.Info("✓ global intercept active — cluster traffic to %q now routes to your laptop", target)

		if upRunConfig != "" {
			emitRunConfig(upRunConfig, target, sync.EnvFile)
		}

		// 6) Optionally launch the local app with the synced env.
		if len(upRun) > 0 {
			res.Launched = true
			out.Result("up", upHumanLaunching(res), res)
			return launchApp(ctx, sync.EnvFile, sync.LocalLogFile, upRun)
		}

		out.Result("up", upHumanNextSteps(res), res)
		return nil
	},
}

// derivePort builds "<local>:<identifier>" from the workload's first Service port,
// using the port number for both local and identifier.
func derivePort(wl *k8s.Workload) string {
	if wl == nil || len(wl.ServicePorts) == 0 {
		return ""
	}
	p := wl.ServicePorts[0].Port
	return fmt.Sprintf("%d:%d", p, p)
}

func localPortOf(port string) int {
	for i := 0; i < len(port); i++ {
		if port[i] == ':' {
			if n, err := strconv.Atoi(port[:i]); err == nil {
				return n
			}
			return 0
		}
	}
	n, _ := strconv.Atoi(port)
	return n
}

// launchApp runs the user command with the synced cluster env merged into the
// environment. When logPath is set, stdout/stderr are tee'd there for `ldbg logs
// local`, and the synthetic LOGGING_FILE_NAME is stripped from the child env so
// the tee is the single writer (a cluster-defined var with a different value is
// left alone). Tee failure degrades to plain passthrough — never blocks launch.
func launchApp(ctx context.Context, envFile, logPath string, argv []string) error {
	extra, err := springconfig.LoadEnvFile(envFile)
	if err != nil {
		return out.Failf("up", "", err)
	}
	env := append(os.Environ(), extra...)
	stdout, stderr := io.Writer(os.Stdout), io.Writer(os.Stderr)
	if logPath != "" {
		env = locallog.StripVar(env, locallog.EnvVar, logPath)
		if f, ferr := locallog.OpenAppend(logPath); ferr != nil {
			out.Info("! local log capture disabled: %v", ferr)
		} else {
			defer f.Close()
			stdout = io.MultiWriter(os.Stdout, f)
			stderr = io.MultiWriter(os.Stderr, f)
		}
	}
	c := exec.CommandContext(ctx, argv[0], argv[1:]...)
	c.Env = env
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, stdout, stderr
	return c.Run()
}

func upHumanNextSteps(r upResult) string {
	s := fmt.Sprintf(`Ready. Start your Spring Boot app on local port %d, then send traffic through the cluster.

  env-file : %s   (Telepresence/IDE EnvFile format)
  port     : %s   (local:remote)`,
		r.LocalPort, r.EnvFile, r.Port)
	if r.LocalLogFile != "" {
		s += fmt.Sprintf("\n  local log: %s   (query with 'ldbg logs local %s')", r.LocalLogFile, r.Target)
	}
	s += fmt.Sprintf(`

Next:
  • IntelliJ/VS Code: set the run config EnvFile to %s, run/debug on port %d
  • or:  set -a; . %s; set +a; ./gradlew bootRun
  • verify:  ldbg test
  • stop:    ldbg down`,
		r.EnvFile, r.LocalPort, r.EnvFile)
	return s
}

func upHumanLaunching(r upResult) string {
	s := fmt.Sprintf("Launching local app on port %d with synced env (%s)…", r.LocalPort, r.EnvFile)
	if r.LocalLogFile != "" {
		s += fmt.Sprintf(" (output tee'd to %s)", r.LocalLogFile)
	}
	return s
}

func init() {
	f := upCmd.Flags()
	f.StringVar(&upPort, "port", "", "local:remote port mapping (default: derive remote from the Service)")
	f.StringArrayVar(&upRun, "run", nil, "command to launch the local app (repeatable); if omitted you run it yourself")
	f.StringVar(&upEnvOut, "env-out", "", "path to write the synced env-file (default: .ldbg/<service>.env)")
	f.BoolVar(&upNoMount, "no-mount", false, "do not mount the pod's secret/configmap volumes locally")
	f.BoolVar(&upKeepAmbient, "keep-ambient", false, "do not exclude the workload from Istio ambient (intercept may be black-holed)")
	f.StringVar(&upRunConfig, "run-config", "", "also generate an IDE run config: 'intellij' or 'vscode'")
	f.BoolVar(&upNoLocalLog, "no-local-log", false, "do not inject the synthetic LOGGING_FILE_NAME var (local log capture for 'ldbg logs local')")
	rootCmd.AddCommand(upCmd)
}
