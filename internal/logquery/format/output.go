// vendored from github.com/hzeng10/log-analysis cli/internal/output @v0.1.1

// Package output renders query results for humans and for Agents.
package format

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/hzeng10/local-debug/internal/logquery/logsql"
)

// Envelope is the stable JSON contract consumed by the Agent. It echoes the
// resolved query and range so the Agent can see exactly what ran, and exposes
// `truncated` so it can detect when it hit the limit and should refine.
type Envelope struct {
	Query     string           `json:"query"`
	Range     logsql.Range     `json:"range"`
	Count     int              `json:"count"`
	Truncated bool             `json:"truncated"`
	Logs      []map[string]any `json:"logs"`
}

// WriteEnvelope renders an Envelope in the requested mode:
//
//	json  - pretty-printed Envelope (default; best for Agents)
//	jsonl - one log object per line (raw VictoriaLogs records)
//	raw   - just the message text, one per line
//	table - aligned time/level/service/message columns (human-friendly)
func WriteEnvelope(w io.Writer, mode string, env Envelope, fields []string) error {
	if len(fields) > 0 {
		for i := range env.Logs {
			env.Logs[i] = project(env.Logs[i], fields)
		}
	}
	switch mode {
	case "", "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(env)
	case "jsonl":
		for _, l := range env.Logs {
			b, err := json.Marshal(l)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintln(w, string(b)); err != nil {
				return err
			}
		}
		return nil
	case "raw":
		for _, l := range env.Logs {
			if _, err := fmt.Fprintln(w, msg(l)); err != nil {
				return err
			}
		}
		return nil
	case "table":
		return writeTable(w, env.Logs)
	default:
		return fmt.Errorf("unknown output mode %q (use json|jsonl|raw|table)", mode)
	}
}

// WriteLog renders a single record (used by `tail`) in a streaming-friendly mode.
func WriteLog(w io.Writer, mode string, l map[string]any) error {
	switch mode {
	case "raw":
		_, err := fmt.Fprintln(w, msg(l))
		return err
	case "table":
		_, err := fmt.Fprintf(w, "%s  %-5s  %-20s  %s\n",
			str(l, "_time"), str(l, "level"), str(l, "service"), msg(l))
		return err
	default: // json/jsonl: one object per line
		b, err := json.Marshal(l)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(b))
		return err
	}
}

// WriteRaw pretty-prints a raw JSON payload (used by stats/fields/values).
func WriteRaw(w io.Writer, payload []byte) error {
	var v any
	if err := json.Unmarshal(payload, &v); err != nil {
		_, err = w.Write(payload) // not JSON? emit as-is
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func writeTable(w io.Writer, logs []map[string]any) error {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tLEVEL\tSERVICE\tMESSAGE")
	for _, l := range logs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			str(l, "_time"), str(l, "level"), str(l, "service"), oneLine(msg(l)))
	}
	return tw.Flush()
}

func project(l map[string]any, fields []string) map[string]any {
	out := make(map[string]any, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if v, ok := l[f]; ok {
			out[f] = v
		}
	}
	return out
}

func msg(l map[string]any) string {
	if v, ok := l["_msg"]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func str(l map[string]any, k string) string {
	if v, ok := l[k]; ok {
		return fmt.Sprint(v)
	}
	return ""
}

func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", "")
}
