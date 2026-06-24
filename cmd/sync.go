package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/output"
	"github.com/hzeng10/local-debug/internal/springconfig"
	"github.com/spf13/cobra"
)

var (
	syncEnvOut string
	syncRunCfg string
)

// syncResult is the --json payload for `ldbg sync`.
type syncResult struct {
	Target    string       `json:"target"`
	Namespace string       `json:"namespace"`
	Kind      string       `json:"kind"`
	Container string       `json:"container"`
	EnvFile   string       `json:"envFile"`
	Written   int          `json:"written"`
	Skipped   int          `json:"skipped"`
	Vars      []k8s.EnvVar `json:"vars"`
}

var syncCmd = &cobra.Command{
	Use:   "sync <service>",
	Short: "Generate a local env-file (and optional IDE run config) from the workload's cluster env",
	Long: `sync reads the target workload's env, envFrom, and the referenced ConfigMaps/Secrets
and writes them as environment variables to an env-file. Spring Boot relaxed binding
applies these over application.yaml/properties with no app changes (works for YAML too).

Secret values are masked in stdout/logs but written in full to the (0600, git-ignored) env-file.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		cl, err := newK8sClient()
		if err != nil {
			return out.Failf("sync", "check --kubeconfig/--context", err)
		}
		ns, err := resolveNamespace(cl)
		if err != nil {
			return out.Failf("sync", "", err)
		}
		res, _, err := syncEnvToFile(ctx, cl, ns, args[0], syncEnvOut)
		if err != nil {
			return out.Failf("sync", "is the service/workload name and namespace correct? RBAC: get on configmaps/secrets", err)
		}
		if syncRunCfg != "" {
			emitRunConfig(syncRunCfg, args[0], res.EnvFile)
		}
		human := fmt.Sprintf("Synced %s/%s (container %q) → %s\n  %d vars written, %d skipped\n%s",
			res.Kind, res.Target, res.Container, res.EnvFile, res.Written, res.Skipped,
			joinLines(springconfig.Summarize(res.Vars, output.Mask)))
		out.Result("sync", human, res)
		return nil
	},
}

// syncEnvToFile resolves the target workload's cluster env and writes it to an
// env-file (default .ldbg/<target>.env). Shared by `sync` and `up`. It also returns
// the resolved Workload so callers can default the intercept port from its Service.
func syncEnvToFile(ctx context.Context, cl *k8s.Client, ns, target, envOut string) (syncResult, *k8s.Workload, error) {
	wl, err := cl.ResolveWorkload(ctx, ns, target)
	if err != nil {
		return syncResult{}, nil, err
	}
	ctr, err := wl.PrimaryContainer()
	if err != nil {
		return syncResult{}, nil, err
	}
	vars, err := cl.ResolveEnv(ctx, ns, ctr)
	if err != nil {
		return syncResult{}, nil, err
	}
	envPath := envOut
	if envPath == "" {
		envPath = filepath.Join(".ldbg", target+".env")
	}
	written, skipped, err := springconfig.WriteEnvFile(envPath, vars)
	if err != nil {
		return syncResult{}, nil, err
	}
	res := syncResult{
		Target: target, Namespace: ns, Kind: wl.Kind, Container: ctr.Name,
		EnvFile: envPath, Written: written, Skipped: skipped, Vars: vars,
	}
	return res, wl, nil
}

func joinLines(lines []string) string {
	s := ""
	for _, l := range lines {
		s += l + "\n"
	}
	return s
}

func init() {
	f := syncCmd.Flags()
	f.StringVar(&syncEnvOut, "env-out", "", "path to write the env-file (default: .ldbg/<service>.env)")
	f.StringVar(&syncRunCfg, "run-config", "", "generate IDE run config: 'intellij' or 'vscode'")
	rootCmd.AddCommand(syncCmd)
}
