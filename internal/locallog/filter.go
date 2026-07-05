package locallog

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hzeng10/local-debug/internal/logquery/logsql"
)

// Filter selects entries by level, keyword, and time window — the local-file
// counterpart of the logs-query vocabulary. Zero values mean "unset".
type Filter struct {
	Level      string
	Query      string // literal terms, AND-matched; "quoted phrases" kept whole
	IgnoreCase bool
	Since      time.Duration
	From, To   time.Time
	Now        func() time.Time // injectable for tests; nil = time.Now
}

// Apply filters entries in file order. When sawTimestamps is false the time
// filters are skipped entirely (the caller reports that degradation); with any
// time filter active, entries without a timestamp are excluded (they are
// pre-logging preamble — stack continuations are already merged into parents).
func (f Filter) Apply(entries []Entry, sawTimestamps bool) []Entry {
	now := time.Now
	if f.Now != nil {
		now = f.Now
	}
	timeActive := sawTimestamps && (f.Since > 0 || !f.From.IsZero() || !f.To.IsZero())
	var cutoff time.Time
	if f.Since > 0 {
		cutoff = now().Add(-f.Since)
	}
	terms := splitTerms(f.Query)

	var outEntries []Entry
	for _, e := range entries {
		if timeActive {
			if !e.HasTime {
				continue
			}
			if f.Since > 0 && e.Time.Before(cutoff) {
				continue
			}
			if !f.From.IsZero() && e.Time.Before(f.From) {
				continue
			}
			if !f.To.IsZero() && e.Time.After(f.To) {
				continue
			}
		}
		if f.Level != "" && !strings.EqualFold(f.Level, e.Level) {
			continue
		}
		if !matchTerms(e.Msg, terms, f.IgnoreCase) {
			continue
		}
		outEntries = append(outEntries, e)
	}
	return outEntries
}

// Tail keeps the last n entries (n <= 0 keeps all); truncated reports a cut.
func Tail(entries []Entry, n int) ([]Entry, bool) {
	if n <= 0 || len(entries) <= n {
		return entries, false
	}
	return entries[len(entries)-n:], true
}

// ParseSince converts a relative window (5m, 4h, 2d, 1w, 1mo, ...) into a
// duration. Validation and month→days conversion are delegated to the vendored
// logsql.NormalizeSince so `logs local --since` accepts exactly what
// `logs query --since` accepts. Note: ParseSince("") returns NormalizeSince's
// 1h default — callers wanting "unset" must guard before calling.
func ParseSince(s string) (time.Duration, error) {
	norm, err := logsql.NormalizeSince(s)
	if err != nil {
		return 0, err
	}
	unit := norm[len(norm)-1]
	n, err := strconv.Atoi(norm[:len(norm)-1])
	if err != nil {
		return 0, fmt.Errorf("invalid --since %q", s)
	}
	switch unit {
	case 's', 'm', 'h':
		return time.ParseDuration(norm)
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case 'y':
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("invalid --since %q", s)
	}
}

// matchTerms requires every term to be a substring of msg (continuations
// included, so stack-frame grep works). No raw-LogsQL escape hatch here —
// ':' and '|' are literal characters in local matching.
func matchTerms(msg string, terms []string, ignoreCase bool) bool {
	if len(terms) == 0 {
		return true
	}
	if ignoreCase {
		msg = strings.ToLower(msg)
	}
	for _, t := range terms {
		if ignoreCase {
			t = strings.ToLower(t)
		}
		if !strings.Contains(msg, t) {
			return false
		}
	}
	return true
}

// splitTerms splits on whitespace keeping "quoted phrases" whole (quotes
// stripped) — same semantics as the vendored logsql splitTerms, reimplemented
// here because that one is unexported and the vendored file stays pristine.
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
