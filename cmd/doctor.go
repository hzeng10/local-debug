package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/logquery/client"
	"github.com/hzeng10/local-debug/internal/logquery/logsql"
	"github.com/hzeng10/local-debug/internal/mesh"
	"github.com/hzeng10/local-debug/internal/tp"
	"github.com/spf13/cobra"
)

var doctorAmbient bool

// check is one preflight result.
type check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail
	Detail string `json:"detail"`
}

type doctorReport struct {
	Checks []check `json:"checks"`
	OK     bool    `json:"ok"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor [service]",
	Short: "Preflight checks: client, cluster reachability, traffic-manager, ambient mode",
	Long: `doctor verifies the environment before an intercept: Telepresence client, cluster
reachability + RBAC, traffic-manager installed, and Istio ambient detection. Give a
[service] to assess whether that specific workload needs the ambient opt-out.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		rep := doctorReport{}
		add := func(n, s, d string) { rep.Checks = append(rep.Checks, check{n, s, d}) }

		// 1) Telepresence client.
		tpc := newTPClient()
		if tpc.Available() {
			ver, _ := tpc.Version(ctx)
			add("telepresence-client", "pass", "found "+strings.TrimSpace(ver))
		} else {
			add("telepresence-client", "fail", "not found; install it or pass --telepresence-bin")
		}

		// 2) Cluster reachability.
		cl, err := newK8sClient()
		if err != nil {
			add("kubeconfig", "fail", err.Error())
		} else if v, perr := cl.Ping(ctx); perr != nil {
			add("cluster-reachable", "fail", perr.Error())
		} else {
			add("cluster-reachable", "pass", "kubernetes "+v)
		}

		// 3) Connection + traffic-manager (best-effort; needs the daemon).
		var tpStatus *tp.Status
		if tpc.Available() {
			if st, serr := tpc.Status(ctx); serr == nil {
				tpStatus = st
				switch {
				case st.Connected && st.ManagerInstalled:
					add("traffic-manager", "pass", "connected; manager installed")
				case st.Connected:
					add("traffic-manager", "warn", "connected but manager state unknown")
				default:
					add("connection", "warn", "not connected; run 'telepresence connect' (sudo/admin once)")
				}
			}
		}

		// 4) Ambient assessment (namespace, and the target workload if given).
		if cl != nil {
			ns := flagNamespace
			if ns == "" {
				ns = cl.DefaultNamespace()
			}
			if nsMode, nerr := cl.NamespaceDataplaneMode(ctx, ns); nerr == nil {
				if nsMode == "" {
					add("ambient-namespace", "pass", fmt.Sprintf("namespace %q is not ambient", ns))
				} else {
					add("ambient-namespace", "pass", fmt.Sprintf("namespace %q dataplane-mode=%s", ns, nsMode))
				}
				if len(args) == 1 {
					assessWorkloadCheck(ctx, cl, ns, args[0], add)
				}
			}
		}

		// 5) Log store + collection coverage (warn-only: the log stack is
		// optional infrastructure — its absence must not block intercepts).
		if cl != nil {
			logStoreChecks(ctx, tpStatus, args, add)
		}

		rep.OK = !hasFail(rep.Checks)
		out.Result("doctor", renderDoctor(rep), rep)
		if !rep.OK {
			return fmt.Errorf("doctor found blocking issues")
		}
		return nil
	},
}

// logStoreChecks verifies the VictoriaLogs store is reachable ('ldbg logs query'
// depends on it) and, given a service, that its logs are actually flowing in —
// catching "not covered by collection" before a debugging session relies on it.
func logStoreChecks(ctx context.Context, st *tp.Status, args []string, add func(string, string, string)) {
	addr, cleanup, source, err := resolveVLogsAddr(ctx, "", "logging", st)
	if err != nil {
		add("log-store", "warn", "VictoriaLogs unreachable — 'ldbg logs query' won't work (deploy the log-analysis stack, or pass --vlogs-addr/VLOGS_ADDR to logs commands)")
		return
	}
	defer cleanup()
	add("log-store", "pass", fmt.Sprintf("VictoriaLogs reachable at %s (%s)", addr, source))

	if len(args) != 1 {
		return
	}
	svc := args[0]
	q, _, err := logsql.Filter{Service: svc, Since: "15m"}.Build()
	if err != nil {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	logs, err := client.New(addr, 10*time.Second).Query(qctx, q, "", 1, 0)
	switch {
	case err != nil:
		add("log-collection", "warn", fmt.Sprintf("query for %q failed: %v", svc, err))
	case len(logs) > 0:
		add("log-collection", "pass", fmt.Sprintf("logs from %q are flowing (seen within 15m)", svc))
	default:
		add("log-collection", "warn", fmt.Sprintf("no logs from %q in the last 15m — collection may not cover it (add the logging.example.com/collect=true label, or deploy the collect-all overlay)", svc))
	}
}

// assessWorkloadCheck resolves the target and reports whether it needs the ambient
// opt-out (which `ldbg up` applies automatically).
func assessWorkloadCheck(ctx context.Context, cl *k8s.Client, ns, target string, add func(string, string, string)) {
	wl, err := cl.ResolveWorkload(ctx, ns, target)
	if err != nil {
		add("target-workload", "fail", err.Error())
		return
	}
	nsMode, _ := cl.NamespaceDataplaneMode(ctx, ns)
	a := mesh.AssessWorkload(nsMode, wl.PodTemplateDataplaneMode())
	switch {
	case a.AlreadyOptedOut:
		add("ambient-workload", "pass", fmt.Sprintf("%s/%s already excluded from ambient", wl.Kind, wl.Name))
	case a.NeedsOptOut:
		add("ambient-workload", "warn", fmt.Sprintf("%s/%s is in ambient; 'ldbg up' will apply dataplane-mode=none", wl.Kind, wl.Name))
	default:
		add("ambient-workload", "pass", fmt.Sprintf("%s/%s not in ambient; no opt-out needed", wl.Kind, wl.Name))
	}
}

func hasFail(cs []check) bool {
	for _, c := range cs {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

func renderDoctor(r doctorReport) string {
	var b strings.Builder
	for _, c := range r.Checks {
		mark := map[string]string{"pass": "✓", "warn": "!", "fail": "✗"}[c.Status]
		fmt.Fprintf(&b, "%s %-22s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Fprintf(&b, "overall: %s", map[bool]string{true: "ok", false: "ISSUES"}[r.OK])
	return b.String()
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorAmbient, "ambient", false, "also run the ambient inbound/outbound validation spike")
	rootCmd.AddCommand(doctorCmd)
}
