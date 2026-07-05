package cmd

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hzeng10/local-debug/internal/logquery/logsql"
	"github.com/hzeng10/local-debug/internal/output"
	"github.com/hzeng10/local-debug/internal/tp"
	"github.com/spf13/cobra"
)

// vlogsService is the in-cluster Service name of the log store (log-analysis
// deploys VictoriaLogs as svc "victorialogs"; exotic setups use --vlogs-addr).
const (
	vlogsService = "victorialogs"
	vlogsPort    = 9428
)

// vlFlags is the filter/connection flag set shared by the `logs` query-family
// subcommands. Each subcommand owns its own instance (cobra local flags).
type vlFlags struct {
	service, pod, container, node, level string
	query                                string
	ignoreCase                           bool
	since, from, to                      string
	limit, offset                        int
	sort, fields, outMode                string
	timeout                              time.Duration
	vlogsAddr, vlogsNamespace            string
}

// register adds the shared flags. withTime gates --since/--from/--to (tail
// streams live and has no window); withPage gates --limit.
func (v *vlFlags) register(cmd *cobra.Command, withTime, withPage bool) {
	f := cmd.Flags()
	f.StringVar(&v.service, "service", "", "filter by service/app name (alternative to the positional arg)")
	f.StringVar(&v.pod, "pod", "", "filter by pod name (includes deleted/restarted pods)")
	f.StringVarP(&v.container, "container", "c", "", "filter by container name")
	f.StringVar(&v.node, "node", "", "filter by node name")
	f.StringVar(&v.level, "level", "", "filter by log level (case-insensitive)")
	f.StringVarP(&v.query, "query", "q", "", "keyword(s) on the message; raw LogsQL when it contains ':' or '|'")
	f.BoolVarP(&v.ignoreCase, "ignore-case", "i", false, "case-insensitive keyword match")
	if withTime {
		f.StringVar(&v.since, "since", "1h", "relative time window (5m, 30m, 1h, 4h, 12h, 1d, 2d, 7d, ...)")
		f.StringVar(&v.from, "from", "", "absolute RFC3339 start (overrides --since)")
		f.StringVar(&v.to, "to", "", "absolute RFC3339 end")
	}
	if withPage {
		f.IntVar(&v.limit, "limit", 1000, "max records to return")
	}
	f.DurationVarP(&v.timeout, "timeout", "t", 30*time.Second, "query timeout")
	f.StringVar(&v.vlogsAddr, "vlogs-addr", "", "VictoriaLogs base URL (default: $VLOGS_ADDR, else auto-resolve)")
	f.StringVar(&v.vlogsNamespace, "vlogs-namespace", "logging", "namespace of the in-cluster log stack")
}

// filter maps the flags onto the vendored query model. Namespace and the
// positional service are applied by the caller (see applyLogDefaults).
func (v *vlFlags) filter() logsql.Filter {
	return logsql.Filter{
		Service:    v.service,
		Pod:        v.pod,
		Container:  v.container,
		Node:       v.node,
		Level:      v.level,
		Since:      v.since,
		From:       v.from,
		To:         v.to,
		Query:      v.query,
		IgnoreCase: v.ignoreCase,
	}
}

func (v *vlFlags) fieldList() []string {
	if strings.TrimSpace(v.fields) == "" {
		return nil
	}
	parts := strings.Split(v.fields, ",")
	out := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// positionalService merges the optional positional [service] with --service:
// the positional wins when only one is given; both set and different is an error.
func (v *vlFlags) positionalService(args []string) error {
	return mergeServiceArg(args, &v.service)
}

// mergeServiceArg is the shared positional-vs---service merge used by the logs
// query family and logs local.
func mergeServiceArg(args []string, service *string) error {
	if len(args) == 0 {
		return nil
	}
	if *service != "" && *service != args[0] {
		return fmt.Errorf("conflicting service names: positional %q vs --service %q", args[0], *service)
	}
	*service = args[0]
	return nil
}

// tpStatusQuick returns telepresence status with a short timeout, or nil when
// the binary is missing or the daemons are down — log queries must keep working
// without telepresence (the port-forward path), so this never errors.
func tpStatusQuick(ctx context.Context) *tp.Status {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, err := newTPClient().Status(ctx)
	if err != nil {
		return nil
	}
	return st
}

// applyLogDefaults fills Filter.Service/Namespace from context: with exactly one
// active intercept an omitted service defaults to it (and, only then, an omitted
// -n defaults to the intercept's namespace). An explicit -n always wins. With
// neither, Namespace stays empty = cluster-wide query — deliberate: the log
// store is cluster-wide and that breadth is a feature, so resolveNamespace()
// (which errors without a namespace) is intentionally not used here.
func applyLogDefaults(f *logsql.Filter, st *tp.Status, nsFlag string) {
	if f.Service == "" && st != nil && len(st.Intercepts) == 1 {
		f.Service = st.Intercepts[0].Name
		if nsFlag == "" {
			f.Namespace = st.Intercepts[0].Namespace
		}
	}
	if nsFlag != "" {
		f.Namespace = nsFlag
	}
}

// queryHint assembles the advisory hint for query results: a refine nudge when
// the limit was hit, and an intercept-gap note when the queried service is being
// intercepted right now (its fresh logs are on the laptop, not in the cluster).
func queryHint(truncated bool, service string, st *tp.Status) string {
	var hints []string
	if truncated {
		hints = append(hints, "results hit --limit: narrow --since, add --level/-q filters, or page with --offset")
	}
	if service != "" && st != nil {
		for _, i := range st.Intercepts {
			if i.Name == service {
				hints = append(hints, fmt.Sprintf("service %q is currently intercepted: its newest logs are on this laptop, not in the cluster store — query them with 'ldbg logs local %s'", service, service))
				break
			}
		}
	}
	return strings.Join(hints, "; ")
}

// vlResolver resolves the VictoriaLogs address per the design's priority order.
// Collaborators are injected so the precedence logic is unit-testable.
type vlResolver struct {
	flagAddr  string
	envAddr   string
	namespace string
	connected bool
	probe     func(addr string) bool
	portFwd   func(ctx context.Context) (addr string, cleanup func(), err error)
}

// resolve returns the address, an optional cleanup (non-nil only for the
// port-forward path), and which source won: "flag", "env", "tunnel", "port-forward".
func (r vlResolver) resolve(ctx context.Context) (addr string, cleanup func(), source string, err error) {
	if a := strings.TrimSpace(r.flagAddr); a != "" {
		return a, nil, "flag", nil
	}
	if a := strings.TrimSpace(r.envAddr); a != "" {
		return a, nil, "env", nil
	}
	if r.connected {
		a := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", vlogsService, r.namespace, vlogsPort)
		if r.probe(a) {
			return a, nil, "tunnel", nil
		}
		// Fall through: a probe false-negative must not strand the user.
	}
	if a, cl, ferr := r.portFwd(ctx); ferr == nil {
		if r.probe(a) {
			return a, cl, "port-forward", nil
		}
		cl()
		err = fmt.Errorf("port-forward established but %s/health did not answer", a)
	} else {
		err = ferr
	}
	return "", nil, "", fmt.Errorf("cannot reach VictoriaLogs: %w", err)
}

// probeHealth is the production probe: GET <addr>/health with a short timeout.
func probeHealth(addr string) bool {
	hc := &http.Client{Timeout: 2 * time.Second}
	resp, err := hc.Get(strings.TrimRight(addr, "/") + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// resolveVLogs wires the production resolver: telepresence tunnel state, the
// real health probe, and a client-go port-forward to svc/victorialogs.
func resolveVLogs(ctx context.Context, v *vlFlags, st *tp.Status) (addr string, cleanup func(), source string, err error) {
	r := vlResolver{
		flagAddr:  v.vlogsAddr,
		envAddr:   os.Getenv("VLOGS_ADDR"),
		namespace: v.vlogsNamespace,
		connected: st != nil && st.Connected,
		probe:     probeHealth,
		portFwd: func(ctx context.Context) (string, func(), error) {
			cl, err := newK8sClient()
			if err != nil {
				return "", nil, err
			}
			pf, err := cl.PortForwardService(ctx, v.vlogsNamespace, vlogsService, vlogsPort)
			if err != nil {
				return "", nil, err
			}
			return pf.Addr(), pf.Close, nil
		},
	}
	addr, cleanup, source, err = r.resolve(ctx)
	if err != nil {
		return "", nil, "", err
	}
	if cleanup == nil {
		cleanup = func() {}
	}
	// To stderr, not out.Info: -o jsonl/raw stdout must stay cleanly pipeable.
	if out.Format == output.Human {
		fmt.Fprintf(out.Err, "victorialogs: %s (%s)\n", addr, source)
	}
	return addr, cleanup, source, nil
}

// vlogsHint is the shared failure hint for unreachable log stores.
const vlogsHint = "run 'telepresence connect', pass --vlogs-addr / set VLOGS_ADDR, or check the log stack is deployed (svc victorialogs, ns logging) — see 'ldbg doctor'"
