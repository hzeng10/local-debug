package cmd

import (
	"bytes"
	"context"
	"strings"

	"github.com/hzeng10/local-debug/internal/logquery/client"
	"github.com/hzeng10/local-debug/internal/logquery/format"
	"github.com/spf13/cobra"
)

var logsQueryFlags vlFlags

var logsQueryCmd = &cobra.Command{
	Use:   "query [service]",
	Short: "Query historical logs from the in-cluster log store (VictoriaLogs)",
	Long: `query searches the cluster's centralized log store by service, namespace, pod,
container, node, level, and keyword, over a relative (--since 5m/30m/1h/4h/12h/1d/2d/7d)
or absolute (--from/--to) time window — including logs of deleted/restarted pods.

With an active intercept, an omitted [service] defaults to the intercepted service.
Reaches VictoriaLogs through the telepresence tunnel when connected, else via an
ephemeral port-forward. Under --json the logq-style envelope
{query,range,count,truncated,logs} is returned as data (-o is ignored).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		const name = "logs query"
		v := &logsQueryFlags
		if err := v.positionalService(args); err != nil {
			return out.Failf(name, "give the service once", err)
		}

		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		st := tpStatusQuick(ctx)
		f := v.filter()
		applyLogDefaults(&f, st, flagNamespace)

		q, rng, err := f.Build()
		if err != nil {
			return out.Failf(name, "check --since/--from/--to format", err)
		}

		addr, cleanup, _, err := resolveVLogs(ctx, v, st)
		if err != nil {
			return out.Failf(name, vlogsHint, err)
		}
		defer cleanup()

		qctx, cancel := context.WithTimeout(ctx, v.timeout)
		defer cancel()
		logs, err := client.New(addr, v.timeout).Query(qctx, q, v.sort, v.limit, v.offset)
		if err != nil {
			return out.Failf(name, vlogsHint, err)
		}

		env := format.Envelope{
			Query:     q,
			Range:     rng,
			Count:     len(logs),
			Truncated: v.limit > 0 && len(logs) >= v.limit,
			Logs:      logs,
		}
		hint := queryHint(env.Truncated, f.Service, st)

		if flagJSON {
			out.ResultHint(name, "", format.Projected(env, v.fieldList()), hint)
			return nil
		}
		var buf bytes.Buffer
		if err := format.WriteEnvelope(&buf, v.outMode, env, v.fieldList()); err != nil {
			return out.Failf(name, "use -o json|jsonl|raw|table", err)
		}
		out.ResultHint(name, strings.TrimRight(buf.String(), "\n"), nil, hint)
		return nil
	},
}

func init() {
	logsQueryFlags.register(logsQueryCmd, true, true)
	f := logsQueryCmd.Flags()
	f.IntVar(&logsQueryFlags.offset, "offset", 0, "records to skip (paging)")
	f.StringVar(&logsQueryFlags.sort, "sort", "_time desc", "sort order, e.g. \"_time asc\"")
	f.StringVar(&logsQueryFlags.fields, "fields", "", "comma-separated field projection")
	f.StringVarP(&logsQueryFlags.outMode, "output", "o", "table", "native output mode: json|jsonl|raw|table (ignored under --json)")
	logsCmd.AddCommand(logsQueryCmd)
}
