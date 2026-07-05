package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/hzeng10/local-debug/internal/logquery/client"
	"github.com/hzeng10/local-debug/internal/logquery/format"
	"github.com/spf13/cobra"
)

var logsTailFlags vlFlags

var logsTailCmd = &cobra.Command{
	Use:   "tail [service]",
	Short: "Stream live logs from the in-cluster log store, with store-side filters",
	Long: `tail follows new logs as VictoriaLogs ingests them, filtered store-side by
service/namespace/pod/container/level/keyword — unlike the bare 'ldbg logs <svc>' pod
tail, it spans all matching pods and needs no time window. Ctrl-C stops it cleanly.

Under --json each record is emitted as one JSON line (an unbounded stream cannot be a
single envelope; pre-stream failures still return the standard error envelope).`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		const name = "logs tail"
		v := &logsTailFlags
		if err := v.positionalService(args); err != nil {
			return out.Failf(name, "give the service once", err)
		}

		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		st := tpStatusQuick(ctx)
		f := v.filter()
		applyLogDefaults(&f, st, flagNamespace)

		q, err := f.BuildLive()
		if err != nil {
			return out.Failf(name, "check the filter flags", err)
		}

		addr, cleanup, _, err := resolveVLogs(ctx, v, st)
		if err != nil {
			return out.Failf(name, vlogsHint, err)
		}
		defer cleanup()

		mode := v.outMode
		if flagJSON {
			mode = "jsonl"
		}
		// Timeout 0: a live stream must not be cut by the HTTP client.
		err = client.New(addr, 0).Tail(ctx, q, func(l client.Log) {
			_ = format.WriteLog(os.Stdout, mode, l)
		})
		if err != nil && (errors.Is(err, context.Canceled) || ctx.Err() != nil) {
			return nil // Ctrl-C is a clean exit
		}
		if err != nil {
			return out.Failf(name, vlogsHint, err)
		}
		return nil
	},
}

func init() {
	logsTailFlags.register(logsTailCmd, false, false)
	logsTailCmd.Flags().StringVarP(&logsTailFlags.outMode, "output", "o", "table", "native output mode: jsonl|raw|table (ignored under --json)")
	logsCmd.AddCommand(logsTailCmd)
}
