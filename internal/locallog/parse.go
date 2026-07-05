package locallog

import (
	"bufio"
	"io"
	"regexp"
	"strings"
	"time"
)

// Entry is one logical log record: a timestamped first line plus any
// continuation lines (stack traces, wrapped output) merged into Msg.
type Entry struct {
	Time    time.Time
	HasTime bool
	Level   string // lowercased trace|debug|info|warn|error|fatal, "" if absent
	Msg     string // full raw first line + "\n"-joined continuations
}

// tsRe recognizes the Spring Boot default timestamp family at line start:
// space or 'T' separator, '.' or ',' decimal mark, 0-9 fraction digits.
var tsRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})(?:[.,](\d{1,9}))?`)

// levelRe finds the log-level token; only the header region after the
// timestamp is searched so level words inside the message don't false-hit.
var levelRe = regexp.MustCompile(`(?i)\b(TRACE|DEBUG|INFO|WARN|ERROR|FATAL)\b`)

// levelWindow bounds how far past the timestamp the level token may sit
// (Spring's header is "<ts> LEVEL <pid> --- [thread] logger : msg").
const levelWindow = 32

// Parse scans r line by line, merging continuation lines (no leading
// timestamp) into the previous entry. tsFormat == "" uses the built-in Spring
// Boot recognizer; otherwise it is a Go time layout tried as a line prefix.
// sawTimestamps reports whether any line carried a parseable timestamp, so
// callers can skip time filters (and say so) on unparseable files.
func Parse(r io.Reader, tsFormat string) (entries []Entry, sawTimestamps bool, err error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024) // tolerate very long lines

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		t, rest, ok := parseTimestamp(line, tsFormat)
		if !ok {
			// Continuation (stack frame, wrapped line, startup banner).
			if len(entries) == 0 {
				entries = append(entries, Entry{Msg: line})
				continue
			}
			entries[len(entries)-1].Msg += "\n" + line
			continue
		}
		sawTimestamps = true
		entries = append(entries, Entry{Time: t, HasTime: true, Level: parseLevel(rest), Msg: line})
	}
	return entries, sawTimestamps, sc.Err()
}

// parseTimestamp returns the parsed time (in the local zone), the remainder of
// the line after the timestamp, and whether the line starts a new entry.
func parseTimestamp(line, tsFormat string) (time.Time, string, bool) {
	if tsFormat != "" {
		if len(line) < len(tsFormat) {
			return time.Time{}, "", false
		}
		t, err := time.ParseInLocation(tsFormat, line[:len(tsFormat)], time.Local)
		if err != nil {
			return time.Time{}, "", false
		}
		return t, line[len(tsFormat):], true
	}
	m := tsRe.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, "", false
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", m[1]+" "+m[2], time.Local)
	if err != nil {
		return time.Time{}, "", false
	}
	if m[3] != "" {
		// Right-pad the fraction to 9 digits = nanoseconds.
		frac := m[3] + strings.Repeat("0", 9-len(m[3]))
		var nanos int
		for _, d := range frac {
			nanos = nanos*10 + int(d-'0')
		}
		t = t.Add(time.Duration(nanos) * time.Nanosecond)
	}
	return t, line[len(m[0]):], true
}

func parseLevel(rest string) string {
	window := rest
	if len(window) > levelWindow {
		window = window[:levelWindow]
	}
	if m := levelRe.FindString(window); m != "" {
		return strings.ToLower(m)
	}
	return ""
}
