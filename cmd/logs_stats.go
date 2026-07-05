package cmd

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/hzeng10/local-debug/internal/logquery/client"
	"github.com/hzeng10/local-debug/internal/logquery/format"
	"github.com/hzeng10/local-debug/internal/logquery/logsql"
	"github.com/spf13/cobra"
)

var logsStatsFlags vlFlags

var logsStatsCmd = &cobra.Command{
	Use:   "stats [expr...]",
	Short: "Aggregate logs with a LogsQL stats expression",
	Long: `stats runs '<filters> | stats <expr>' against the log store. The default
expression counts records per service; combine with the shared filter flags, e.g.:

  ldbg logs stats "by (level) count() as c" --service orders --since 10m

A zeroed error count after a fix is a machine-readable verification signal.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		const name = "logs stats"
		v := &logsStatsFlags

		expr := strings.TrimSpace(strings.Join(args, " "))
		if expr == "" {
			expr = "by (service) count() as count"
		}

		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		st := tpStatusQuick(ctx)
		f := v.filter()
		applyLogDefaults(&f, st, flagNamespace)

		base, rng, err := f.Build()
		if err != nil {
			return out.Failf(name, "check --since/--from/--to format", err)
		}
		q := base + " | stats " + expr

		addr, cleanup, _, err := resolveVLogs(ctx, v, st)
		if err != nil {
			return out.Failf(name, vlogsHint, err)
		}
		defer cleanup()

		qctx, cancel := context.WithTimeout(ctx, v.timeout)
		defer cancel()
		raw, err := client.New(addr, v.timeout).Stats(qctx, q)
		if err != nil {
			return out.Failf(name, vlogsHint, err)
		}

		if flagJSON {
			out.Result(name, "", struct {
				Query string          `json:"query"`
				Range logsql.Range    `json:"range"`
				Stats json.RawMessage `json:"stats"`
			}{q, rng, raw})
			return nil
		}
		return format.WriteRaw(os.Stdout, raw)
	},
}

func init() {
	logsStatsFlags.register(logsStatsCmd, true, false)
	logsCmd.AddCommand(logsStatsCmd)
}
