// vendored from github.com/hzeng10/log-analysis cli/internal/logsql @v0.1.1

// Package logsql turns the unified filter model (service/pod/namespace/time/
// keyword) into a LogsQL query string for VictoriaLogs.
//
// Query semantics (confirmed design):
//   - All structured filters are optional and AND-combined. An omitted dimension
//     is left unconstrained, so `--since 1h -q Exception` searches every
//     collected service.
//   - The -q keyword is matched against the message field (_msg) only, and is
//     CASE-SENSITIVE by default. IgnoreCase wraps each term in LogsQL i(...).
//   - If -q contains a ':' (field filter) or '|' (pipe), the whole value is
//     treated as raw LogsQL and passed through unchanged.
package logsql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Filter is the unified, backend-agnostic query model shared by the CLI and the
// web UI's mental model.
type Filter struct {
	Service    string
	Namespace  string
	Pod        string
	Container  string
	Node       string
	Level      string
	Since      string // relative range, e.g. "1h", "12h", "1d", "1w", "1mo"
	From       string // absolute RFC3339 start (overrides Since)
	To         string // absolute RFC3339 end
	Query      string // keyword(s) on _msg, or raw LogsQL
	IgnoreCase bool
}

// Range describes the resolved time window, echoed back to the caller so an
// Agent can see exactly what was queried.
type Range struct {
	Since string `json:"since,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

var sinceRe = regexp.MustCompile(`^\d+[smhdwy]$`)

// Build returns the LogsQL query (with a time filter) plus the resolved range.
func (f Filter) Build() (string, Range, error) {
	return f.build(true)
}

// BuildLive returns the LogsQL query WITHOUT a time filter, for live tailing
// (the tail endpoint streams new logs as they arrive).
func (f Filter) BuildLive() (string, error) {
	q, _, err := f.build(false)
	return q, err
}

func (f Filter) build(includeTime bool) (string, Range, error) {
	var parts []string
	var rng Range

	if includeTime {
		tf, r, err := f.timeFilter()
		if err != nil {
			return "", rng, err
		}
		rng = r
		parts = append(parts, tf)
	}

	add := func(field, val string) {
		if strings.TrimSpace(val) != "" {
			parts = append(parts, fmt.Sprintf("%s:=%s", field, quote(val)))
		}
	}
	add("namespace", f.Namespace)
	add("service", f.Service)
	add("pod", f.Pod)
	add("container", f.Container)
	add("node", f.Node)
	add("level", strings.ToLower(f.Level))

	if kw := f.keywordExpr(); kw != "" {
		parts = append(parts, kw)
	}

	if len(parts) == 0 {
		// Match everything (LogsQL requires a non-empty query).
		parts = append(parts, "*")
	}
	return strings.Join(parts, " "), rng, nil
}

func (f Filter) timeFilter() (string, Range, error) {
	if f.From != "" || f.To != "" {
		switch {
		case f.From != "" && f.To != "":
			return fmt.Sprintf("_time:[%s, %s]", f.From, f.To), Range{From: f.From, To: f.To}, nil
		case f.From != "":
			return fmt.Sprintf("_time:>=%s", f.From), Range{From: f.From}, nil
		default:
			return fmt.Sprintf("_time:<=%s", f.To), Range{To: f.To}, nil
		}
	}
	since, err := NormalizeSince(f.Since)
	if err != nil {
		return "", Range{}, err
	}
	return "_time:" + since, Range{Since: since}, nil
}

func (f Filter) keywordExpr() string {
	q := strings.TrimSpace(f.Query)
	if q == "" {
		return ""
	}
	// Strong signals that the user wrote raw LogsQL rather than keywords.
	if strings.Contains(q, "|") || strings.Contains(q, ":") {
		return "(" + q + ")"
	}
	terms := splitTerms(q)
	for i, t := range terms {
		if f.IgnoreCase {
			terms[i] = "i(" + quote(t) + ")"
		} else {
			terms[i] = quote(t)
		}
	}
	if len(terms) == 1 {
		return terms[0]
	}
	return "(" + strings.Join(terms, " ") + ")"
}

// NormalizeSince validates a relative range and converts month units to days
// (LogsQL has no month unit). "1mo"/"2months" -> "30d"/"60d".
func NormalizeSince(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return "1h", nil
	}
	for _, suf := range []string{"months", "month", "mo"} {
		if strings.HasSuffix(s, suf) {
			n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(s, suf)))
			if err != nil || n <= 0 {
				return "", fmt.Errorf("invalid --since %q", s)
			}
			return strconv.Itoa(n*30) + "d", nil
		}
	}
	if !sinceRe.MatchString(s) {
		return "", fmt.Errorf("invalid --since %q (use e.g. 30m, 1h, 12h, 1d, 1w, 1mo)", s)
	}
	return s, nil
}

// splitTerms splits a keyword string on whitespace, keeping "quoted phrases"
// together and stripping the surrounding quotes.
func splitTerms(s string) []string {
	var terms []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			terms = append(terms, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return terms
}

// quote wraps a value as a LogsQL quoted token, escaping backslashes and quotes.
func quote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
