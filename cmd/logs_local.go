package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hzeng10/local-debug/internal/locallog"
	"github.com/hzeng10/local-debug/internal/logquery/format"
	"github.com/hzeng10/local-debug/internal/logquery/logsql"
	"github.com/hzeng10/local-debug/internal/output"
	"github.com/spf13/cobra"
)

var logsLocalFlags struct {
	service, file             string
	query                     string
	ignoreCase                bool
	level                     string
	since, from, to           string
	tail                      int
	tsFormat, fields, outMode string
}

// localLogEnvelope is the --json payload: the logq envelope shape plus the
// source/file identification (format.Envelope is vendored — left untouched).
type localLogEnvelope struct {
	Source    string           `json:"source"` // always "local-file"
	File      string           `json:"file"`
	Range     logsql.Range     `json:"range"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated"`
	Logs      []map[string]any `json:"logs"`
}

var logsLocalCmd = &cobra.Command{
	Use:   "local [service]",
	Short: "Query the laptop-side log file captured during an intercept",
	Long: `local searches .ldbg/logs/<service>.log — the file the local app writes while its
service is intercepted (via 'ldbg up --run' tee, or the injected LOGGING_FILE_NAME for
IDE runs). It fills the cluster-store gap that 'logs query' hints about.

Timestamps are parsed from the Spring Boot default pattern in the laptop's local time
zone (--ts-format overrides with a Go layout); lines without a timestamp (stack traces)
are merged into the entry above, so -q matches inside stack frames. Unlike 'logs query',
-q is always literal substrings (AND, "quoted phrases" kept whole) — ':' and '|' have no
special meaning. The file accumulates across app restarts until 'ldbg down' removes it.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		const name = "logs local"
		lf := &logsLocalFlags
		if err := mergeServiceArg(args, &lf.service); err != nil {
			return out.Failf(name, "give the service once", err)
		}

		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}

		// Resolve the file: --file > service > single active intercept.
		path := lf.file
		if path == "" && lf.service == "" {
			if st := tpStatusQuick(ctx); st != nil && len(st.Intercepts) == 1 {
				lf.service = st.Intercepts[0].Name
			}
		}
		if path == "" && lf.service != "" {
			p, err := locallog.PathFor(lf.service)
			if err != nil {
				return out.Failf(name, "", err)
			}
			path = p
		}
		if path == "" {
			return out.Failf(name, "give a service, --file, or run inside an active intercept",
				fmt.Errorf("no service or file to read"))
		}
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}

		f, err := os.Open(path)
		if err != nil {
			return out.Failf(name,
				fmt.Sprintf("no local log at %s — likely causes: (1) the app ships a fully custom logback(-spring).xml that ignores logging.file.name; (2) sync ran with --no-local-log; (3) the IDE run config predates the injection — re-run 'ldbg sync %s' and restart the app (with 'ldbg up --run' the file is tee'd automatically)", path, lf.service),
				err)
		}
		defer f.Close()

		entries, sawTimestamps, err := locallog.Parse(f, lf.tsFormat)
		if err != nil {
			return out.Failf(name, "", err)
		}

		// Build the filter; --since only when explicitly given ("" = whole file).
		filt := locallog.Filter{Level: lf.level, Query: lf.query, IgnoreCase: lf.ignoreCase}
		var rng logsql.Range
		if lf.since != "" && lf.from == "" && lf.to == "" {
			d, serr := locallog.ParseSince(lf.since)
			if serr != nil {
				return out.Failf(name, "check --since format (5m, 4h, 2d, 1w, ...)", serr)
			}
			filt.Since = d
			rng.Since, _ = logsql.NormalizeSince(lf.since)
		}
		if lf.from != "" {
			t, perr := time.Parse(time.RFC3339, lf.from)
			if perr != nil {
				return out.Failf(name, "--from must be RFC3339", perr)
			}
			filt.From, rng.From = t, lf.from
		}
		if lf.to != "" {
			t, perr := time.Parse(time.RFC3339, lf.to)
			if perr != nil {
				return out.Failf(name, "--to must be RFC3339", perr)
			}
			filt.To, rng.To = t, lf.to
		}

		matched := filt.Apply(entries, sawTimestamps)
		kept, truncated := locallog.Tail(matched, lf.tail)

		logs := make([]map[string]any, 0, len(kept))
		for _, e := range kept {
			m := map[string]any{"_msg": e.Msg}
			if e.HasTime {
				m["_time"] = e.Time.Format(time.RFC3339Nano)
			}
			if e.Level != "" {
				m["level"] = e.Level
			}
			if lf.service != "" {
				m["service"] = lf.service
			}
			logs = append(logs, m)
		}

		var hints []string
		timeRequested := lf.since != "" || lf.from != "" || lf.to != ""
		if !sawTimestamps && timeRequested {
			hints = append(hints, fmt.Sprintf("no parseable timestamps in %s — time filters not applied; pass --ts-format matching the app's log pattern", path))
		}
		if truncated {
			hints = append(hints, "results hit --tail: increase --tail or narrow --since/-q")
		}
		hint := strings.Join(hints, "; ")

		env := format.Envelope{Range: rng, Count: len(logs), Truncated: truncated, Logs: logs}
		if flagJSON {
			proj := format.Projected(env, splitFields(lf.fields))
			out.ResultHint(name, "", localLogEnvelope{
				Source: "local-file", File: path,
				Range: proj.Range, Count: proj.Count, Truncated: proj.Truncated, Logs: proj.Logs,
			}, hint)
			return nil
		}
		if out.Format == output.Human {
			fmt.Fprintf(out.Err, "local log: %s (source=local-file)\n", path)
		}
		var buf bytes.Buffer
		if err := format.WriteEnvelope(&buf, lf.outMode, env, splitFields(lf.fields)); err != nil {
			return out.Failf(name, "use -o json|jsonl|raw|table", err)
		}
		out.ResultHint(name, strings.TrimRight(buf.String(), "\n"), nil, hint)
		return nil
	},
}

// splitFields is fieldList for the local flag struct.
func splitFields(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	fields := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			fields = append(fields, p)
		}
	}
	return fields
}

func init() {
	lf := &logsLocalFlags
	f := logsLocalCmd.Flags()
	f.StringVar(&lf.service, "service", "", "service whose local log to read (alternative to the positional arg)")
	f.StringVar(&lf.file, "file", "", "explicit log file path (wins over the service)")
	f.StringVarP(&lf.query, "query", "q", "", "literal keyword(s), AND-matched; \"quoted phrases\" kept whole")
	f.BoolVarP(&lf.ignoreCase, "ignore-case", "i", false, "case-insensitive keyword match")
	f.StringVar(&lf.level, "level", "", "filter by parsed log level (case-insensitive)")
	f.StringVar(&lf.since, "since", "", "relative time window (5m, 4h, 2d, ...); default: whole file")
	f.StringVar(&lf.from, "from", "", "absolute RFC3339 start (overrides --since)")
	f.StringVar(&lf.to, "to", "", "absolute RFC3339 end")
	f.IntVar(&lf.tail, "tail", 0, "keep only the last N matching entries (0 = all)")
	f.StringVar(&lf.tsFormat, "ts-format", "", "Go time layout overriding the Spring Boot default timestamp recognizer")
	f.StringVar(&lf.fields, "fields", "", "comma-separated field projection")
	f.StringVarP(&lf.outMode, "output", "o", "table", "native output mode: json|jsonl|raw|table (ignored under --json)")
	logsCmd.AddCommand(logsLocalCmd)
}
