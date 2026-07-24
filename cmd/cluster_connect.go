package cmd

// Connectivity helpers for the "remote-only kubectl" topology: the laptop has no
// kubectl/kubeconfig and reaches the cluster only through an SSH tunnel to a
// bastion where kubectl runs. `cluster probe` confirms what a given bridge can
// actually carry (REST vs the port-forward upgrade); `cluster kubeconfig` emits a
// minimal kubeconfig for a tunneled `kubectl proxy`; `cluster tunnel` prints the
// exact bastion + `ssh -L` commands.

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// probeStage is one line of the `cluster probe` report.
type probeStage struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | skip
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

type clusterProbeResult struct {
	Stages []probeStage `json:"stages"`
	OK     bool         `json:"ok"`
}

var (
	probeVlogsAddr   string
	probePFNamespace string
	probePFService   string
	probePFPort      int
)

var clusterProbeCmd = &cobra.Command{
	Use:   "probe",
	Short: "Confirm a tunneled/proxied cluster bridge actually carries the connection ldbg needs",
	Long: `probe reports, stage by stage, what a given cluster bridge can carry — the check to
run when the laptop reaches the cluster indirectly (SSH tunnel to a bastion, a
'kubectl proxy', etc.) and you need to know whether ldbg will work before relying on it:

  api          REST + auth reach the apiserver (Ping)
  rbac         the credentials can read pods in the namespace
  port-forward the bridge carries the SPDY/WebSocket upgrade port-forward/logs need
  log-store    (with --vlogs-addr) VictoriaLogs answers /health through the tunnel

The decisive stage is port-forward, which needs a stream (SPDY/WebSocket) upgrade.
'kubectl proxy' is upgrade-aware and usually carries it, but a given bridge may not —
it depends on the kubectl/kube version and any extra proxy or load-balancer in the path.
When port-forward fails while 'api' passes, port-forward on the bastion and tunnel a
plain TCP port over 'ssh -L' instead (see 'ldbg cluster tunnel'), which never depends on
the upgrade surviving the bridge.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		res := clusterProbeResult{}
		add := func(n, s, d, h string) { res.Stages = append(res.Stages, probeStage{n, s, d, h}) }

		cl, err := newK8sClient()
		switch {
		case err != nil:
			add("api", "fail", err.Error(),
				"check --kubeconfig/--context; for a 'kubectl proxy' bridge point --kubeconfig at a localhost kubeconfig (see 'ldbg cluster kubeconfig')")
		default:
			if v, perr := cl.Ping(ctx); perr != nil {
				add("api", "fail", perr.Error(),
					"is the apiserver reachable through the tunnel? bastion: 'kubectl proxy --port=8001'; laptop: 'ssh -L 8001:127.0.0.1:8001 user@bastion'")
			} else {
				add("api", "pass", "kubernetes "+v+" reachable (REST + auth ok)", "")

				ns := flagNamespace
				if ns == "" {
					ns = cl.DefaultNamespace()
				}
				if n, rerr := cl.ProbeReadPods(ctx, ns); rerr != nil {
					add("rbac", "warn", fmt.Sprintf("cannot list pods in %q: %v", ns, rerr),
						"the bastion credentials may lack read RBAC here; pass -n <a namespace you can read>")
				} else {
					add("rbac", "pass", fmt.Sprintf("listed pods in %q (%d returned, capped at 1)", ns, n), "")
				}

				// The make-or-break stage. May take up to ~10s when a proxy accepts
				// the connection but never completes the stream upgrade.
				if pf, ferr := cl.PortForwardService(ctx, probePFNamespace, probePFService, probePFPort); ferr != nil {
					add("port-forward", "fail",
						fmt.Sprintf("port-forward to %s/%s:%d failed: %v", probePFNamespace, probePFService, probePFPort, ferr),
						"if 'api' passed but this failed, this bridge doesn't carry the port-forward stream upgrade (some proxies / older versions don't) — or the log stack isn't deployed / the service name is wrong (read the error). Fallback: port-forward on the bastion and tunnel the plain port with 'ssh -L' ('ldbg cluster tunnel'), which doesn't depend on the upgrade.")
				} else {
					pf.Close()
					add("port-forward", "pass",
						fmt.Sprintf("port-forward to %s/%s:%d works — the bridge carries the stream upgrade", probePFNamespace, probePFService, probePFPort), "")
				}
			}
		}

		// Independent of any kubeconfig: probe the log store directly (the B1 path,
		// where logs reach a bastion-side 'kubectl port-forward' over 'ssh -L').
		if addr := strings.TrimSpace(probeVlogsAddr); addr != "" {
			if probeHealth(addr) {
				add("log-store", "pass", fmt.Sprintf("VictoriaLogs /health ok at %s", addr),
					fmt.Sprintf("'ldbg logs query --vlogs-addr %s <svc>' will work with no kubeconfig", addr))
			} else {
				add("log-store", "fail", fmt.Sprintf("VictoriaLogs /health did not answer at %s", addr),
					"is the tunnel up? bastion: 'kubectl port-forward -n logging svc/victorialogs 9428:9428'; laptop: 'ssh -L 9428:127.0.0.1:9428 user@bastion'")
			}
		}

		res.OK = !probeHasFail(res.Stages)
		out.Result("cluster probe", renderProbe(res), res)
		if !res.OK {
			return fmt.Errorf("cluster probe found blocking issues")
		}
		return nil
	},
}

func probeHasFail(ss []probeStage) bool {
	for _, s := range ss {
		if s.Status == "fail" {
			return true
		}
	}
	return false
}

func renderProbe(r clusterProbeResult) string {
	var b strings.Builder
	marks := map[string]string{"pass": "✓", "warn": "!", "fail": "✗", "skip": "·"}
	for _, s := range r.Stages {
		fmt.Fprintf(&b, "%s %-13s %s\n", marks[s.Status], s.Name, s.Detail)
		if s.Hint != "" && s.Status != "pass" {
			fmt.Fprintf(&b, "    ↳ %s\n", s.Hint)
		}
	}
	fmt.Fprintf(&b, "overall: %s", map[bool]string{true: "ok", false: "ISSUES"}[r.OK])
	return b.String()
}

// --- cluster kubeconfig ------------------------------------------------------

var (
	kcAPI     string
	kcOut     string
	kcContext string
)

var clusterKubeconfigCmd = &cobra.Command{
	Use:   "kubeconfig",
	Short: "Emit a minimal kubeconfig pointing at a local (tunneled) API endpoint",
	Long: `kubeconfig writes a credential-free kubeconfig whose server is a local URL — the
laptop end of an 'ssh -L' tunnel to a bastion 'kubectl proxy'. The proxy authenticates
with the bastion's own credentials, so the client needs none.

  bastion:  kubectl proxy --port=8001 --address 127.0.0.1
  laptop :  ssh -L 8001:127.0.0.1:8001 user@bastion
  laptop :  ldbg cluster kubeconfig --api http://127.0.0.1:8001 --out proxy.kubeconfig
  laptop :  ldbg --kubeconfig proxy.kubeconfig sync <service>

This carries client-go REST calls (sync, resolve, the ambient patch) only — not
port-forward or a full intercept (verify with 'ldbg cluster probe').`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		api := strings.TrimSpace(kcAPI)
		if api == "" {
			return out.Failf("cluster kubeconfig",
				"pass --api http://127.0.0.1:8001 (the local end of your 'ssh -L' tunnel to 'kubectl proxy')",
				fmt.Errorf("--api is required"))
		}
		kc := minimalKubeconfig(api, kcContext)
		if kcOut != "" {
			if err := os.WriteFile(kcOut, []byte(kc), 0600); err != nil {
				return out.Failf("cluster kubeconfig", "", err)
			}
			human := fmt.Sprintf("wrote %s → server %s\nuse it: ldbg --kubeconfig %s <command>", kcOut, api, kcOut)
			out.Result("cluster kubeconfig", human, map[string]string{"path": kcOut, "server": api})
			return nil
		}
		out.Result("cluster kubeconfig", kc, map[string]string{"server": api, "kubeconfig": kc})
		return nil
	},
}

// minimalKubeconfig builds a credential-free kubeconfig for a local proxy server.
// TLS verification is skipped only when the endpoint is https (a direct apiserver
// tunnel); a 'kubectl proxy' is plain http and needs nothing.
func minimalKubeconfig(server, ctxName string) string {
	if strings.TrimSpace(ctxName) == "" {
		ctxName = "ldbg-proxy"
	}
	tls := ""
	if strings.HasPrefix(server, "https://") {
		tls = "\n    insecure-skip-tls-verify: true"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: %[1]s
  cluster:
    server: %[2]s%[3]s
contexts:
- name: %[1]s
  context:
    cluster: %[1]s
    user: %[1]s
current-context: %[1]s
users:
- name: %[1]s
  user: {}
`, ctxName, server, tls)
}

// --- cluster tunnel ----------------------------------------------------------

var (
	tunBastion   string
	tunVlogsNS   string
	tunVlogsSvc  string
	tunVlogsPort int
	tunAPIPort   int
)

var clusterTunnelCmd = &cobra.Command{
	Use:   "tunnel",
	Short: "Print the bastion + 'ssh -L' commands to reach logs (and the API) from the laptop",
	Long: `tunnel prints copy-paste commands for the "kubectl lives only on a bastion" setup:
a bastion-side port-forward/proxy plus the matching laptop 'ssh -L' and 'ldbg' command.
Windows 11 ships the OpenSSH client, so 'ssh -L' works out of the box.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		bastion := strings.TrimSpace(tunBastion)
		if bastion == "" {
			bastion = "user@bastion"
		}
		logs := fmt.Sprintf(`# logs (robust — no local kubectl, no proxy):
  bastion:  kubectl port-forward -n %[1]s svc/%[2]s %[3]d:%[3]d --address 127.0.0.1
  laptop :  ssh -L %[3]d:127.0.0.1:%[3]d %[4]s
  laptop :  ldbg logs query --vlogs-addr http://127.0.0.1:%[3]d <service> --since 30m
  verify :  ldbg cluster probe --vlogs-addr http://127.0.0.1:%[3]d`,
			tunVlogsNS, tunVlogsSvc, tunVlogsPort, bastion)
		api := fmt.Sprintf(`# API / REST (sync + read-only; NOT a full intercept):
  bastion:  kubectl proxy --port=%[1]d --address 127.0.0.1
  laptop :  ssh -L %[1]d:127.0.0.1:%[1]d %[2]s
  laptop :  ldbg cluster kubeconfig --api http://127.0.0.1:%[1]d --out proxy.kubeconfig
  laptop :  ldbg --kubeconfig proxy.kubeconfig sync <service>
  verify :  ldbg cluster probe --kubeconfig proxy.kubeconfig`,
			tunAPIPort, bastion)
		human := logs + "\n\n" + api + "\n\nNote: for FULL capability (incl. a telepresence intercept, 'ldbg up') pull real\ncredentials off the node instead: 'ldbg cluster fetch-kubeconfig --ssh " + bastion + "'\nthen tunnel the apiserver port directly with 'ssh -L' and verify with 'ldbg cluster probe'.\nOtherwise run the intercept on the bastion; the proxy bridge above is REST-only."
		out.Result("cluster tunnel", human, map[string]any{
			"bastion":   bastion,
			"logsSteps": logs,
			"apiSteps":  api,
		})
		return nil
	},
}

func init() {
	pf := clusterProbeCmd.Flags()
	pf.StringVar(&probeVlogsAddr, "vlogs-addr", "", "also health-check a (tunneled) VictoriaLogs base URL, e.g. http://127.0.0.1:9428")
	pf.StringVar(&probePFNamespace, "pf-namespace", "logging", "namespace of the service used for the port-forward capability test")
	pf.StringVar(&probePFService, "pf-service", vlogsService, "service used for the port-forward capability test")
	pf.IntVar(&probePFPort, "pf-port", vlogsPort, "port used for the port-forward capability test")

	kf := clusterKubeconfigCmd.Flags()
	kf.StringVar(&kcAPI, "api", "", "local API URL to point at (e.g. http://127.0.0.1:8001)")
	kf.StringVar(&kcOut, "out", "", "write the kubeconfig to this file (0600); default: print to stdout")
	kf.StringVar(&kcContext, "context-name", "ldbg-proxy", "name for the generated cluster/context/user")

	tf := clusterTunnelCmd.Flags()
	tf.StringVar(&tunBastion, "bastion", "", "SSH target of the bastion where kubectl runs (user@host)")
	tf.StringVar(&tunVlogsNS, "vlogs-namespace", "logging", "namespace of the in-cluster log stack")
	tf.StringVar(&tunVlogsSvc, "vlogs-service", vlogsService, "VictoriaLogs service name")
	tf.IntVar(&tunVlogsPort, "vlogs-port", vlogsPort, "VictoriaLogs port")
	tf.IntVar(&tunAPIPort, "api-port", 8001, "local/bastion port for 'kubectl proxy'")

	clusterCmd.AddCommand(clusterProbeCmd, clusterKubeconfigCmd, clusterTunnelCmd)
}
