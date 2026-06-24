package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	testPath string
	testPort int
)

type testResult struct {
	URL         string `json:"url"`
	FromCluster bool   `json:"fromClusterOrigin"`
	Succeeded   bool   `json:"succeeded"`
	ExitCode    int32  `json:"exitCode"`
	Body        string `json:"body"`
}

var testCmd = &cobra.Command{
	Use:   "test <service>",
	Short: "Send a request through the cluster path and show what answered (proves takeover)",
	Long: `test runs a short-lived curl Pod INSIDE the cluster that GETs the target Service,
so the request originates from a real in-cluster client. If the global intercept is
active, the response comes from your local process. Designed for ClaudeCode with --json.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		target := args[0]

		cl, err := newK8sClient()
		if err != nil {
			return out.Failf("test", "check --kubeconfig/--context", err)
		}
		ns, err := resolveNamespace(cl)
		if err != nil {
			return out.Failf("test", "", err)
		}

		port := testPort
		if port == 0 {
			wl, werr := cl.ResolveWorkload(ctx, ns, target)
			if werr != nil {
				return out.Failf("test", "pass --port", werr)
			}
			if len(wl.ServicePorts) == 0 {
				return out.Failf("test", "pass --port", fmt.Errorf("no Service port for %q", target))
			}
			port = int(wl.ServicePorts[0].Port)
		}

		url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d%s", target, ns, port, ensureLeadingSlash(testPath))
		out.Info("… probing %s from an in-cluster pod", url)
		pr, err := cl.ProbeFromCluster(ctx, ns, url, "", 60*time.Second)
		if err != nil {
			return out.Failf("test", "is the service up and the intercept active?", err)
		}

		res := testResult{URL: url, FromCluster: true, Succeeded: pr.Succeeded, ExitCode: pr.ExitCode, Body: strings.TrimSpace(pr.Body)}
		human := fmt.Sprintf("in-cluster GET %s\n  succeeded=%v exit=%d\n  response: %s\n(if this is your local process's response, the global intercept works)",
			url, res.Succeeded, res.ExitCode, truncate(res.Body, 600))
		out.Result("test", human, res)
		if !res.Succeeded {
			return fmt.Errorf("probe failed (exit %d)", res.ExitCode)
		}
		return nil
	},
}

func ensureLeadingSlash(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func init() {
	f := testCmd.Flags()
	f.StringVar(&testPath, "path", "/", "request path to send through the cluster")
	f.IntVar(&testPort, "port", 0, "service port (default: derive from the Service)")
	rootCmd.AddCommand(testCmd)
}
