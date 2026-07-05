package locallog

import (
	"strings"
	"testing"
	"time"
)

func TestParseSpringDefault(t *testing.T) {
	in := "2026-07-05 10:00:00.123  INFO 4242 --- [main] c.e.Svc : hi\n"
	entries, saw, err := Parse(strings.NewReader(in), "")
	if err != nil || !saw || len(entries) != 1 {
		t.Fatalf("entries=%d saw=%v err=%v", len(entries), saw, err)
	}
	e := entries[0]
	want := time.Date(2026, 7, 5, 10, 0, 0, 123_000_000, time.Local)
	if !e.HasTime || !e.Time.Equal(want) {
		t.Errorf("time = %v, want %v", e.Time, want)
	}
	if e.Level != "info" {
		t.Errorf("level = %q", e.Level)
	}
	if e.Msg != strings.TrimSuffix(in, "\n") {
		t.Errorf("Msg must be the full raw line, got %q", e.Msg)
	}
}

func TestParseTSeparatorAndDecimalComma(t *testing.T) {
	in := "2026-07-05T10:00:00,123 ERROR 1 --- [main] c.e.Svc : boom\n"
	entries, saw, _ := Parse(strings.NewReader(in), "")
	if !saw || len(entries) != 1 || entries[0].Level != "error" {
		t.Fatalf("entries=%v saw=%v", entries, saw)
	}
	if entries[0].Time.Nanosecond() != 123_000_000 {
		t.Errorf("nanos = %d", entries[0].Time.Nanosecond())
	}
}

func TestParseFractionWidths(t *testing.T) {
	cases := map[string]int{
		"2026-07-05 10:00:00 INFO x\n":           0,
		"2026-07-05 10:00:00.5 INFO x\n":         500_000_000,
		"2026-07-05 10:00:00.123456789 INFO x\n": 123_456_789,
	}
	for in, nanos := range cases {
		entries, saw, err := Parse(strings.NewReader(in), "")
		if err != nil || !saw || len(entries) != 1 {
			t.Fatalf("%q: entries=%d saw=%v err=%v", in, len(entries), saw, err)
		}
		if entries[0].Time.Nanosecond() != nanos {
			t.Errorf("%q: nanos = %d, want %d", in, entries[0].Time.Nanosecond(), nanos)
		}
	}
}

func TestParseStackTraceMerges(t *testing.T) {
	in := `2026-07-05 10:00:00.000 ERROR 1 --- [main] c.e.Svc : boom
java.lang.IllegalStateException: kaput
	at com.example.Svc.handle(Svc.java:42)
	at com.example.Svc.main(Svc.java:10)
Caused by: java.io.IOException: disk
2026-07-05 10:00:01.000  INFO 1 --- [main] c.e.Svc : recovered
`
	entries, _, _ := Parse(strings.NewReader(in), "")
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	if got := strings.Count(entries[0].Msg, "\n"); got != 4 {
		t.Errorf("stack entry should hold 4 embedded newlines, got %d: %q", got, entries[0].Msg)
	}
	if !strings.Contains(entries[0].Msg, "Caused by") {
		t.Errorf("continuation lost: %q", entries[0].Msg)
	}
}

func TestParseLeadingBanner(t *testing.T) {
	in := "  .   ____          _\n :: Spring Boot ::  (v3.2.0)\n\n2026-07-05 10:00:00.000  INFO 1 --- [main] c.e.App : Started\n"
	entries, saw, _ := Parse(strings.NewReader(in), "")
	if !saw || len(entries) != 2 {
		t.Fatalf("entries=%d saw=%v", len(entries), saw)
	}
	if entries[0].HasTime {
		t.Error("banner entry must have HasTime=false")
	}
	if !entries[1].HasTime {
		t.Error("timestamped entry lost")
	}
}

func TestParseNoTimestampsAtAll(t *testing.T) {
	entries, saw, _ := Parse(strings.NewReader("just\nplain\nlines\n"), "")
	if saw {
		t.Error("sawTimestamps must be false")
	}
	if len(entries) != 1 {
		t.Errorf("all lines should merge into one entry, got %d", len(entries))
	}
}

func TestParseTSFormatOverride(t *testing.T) {
	layout := "02/01/2006 15:04:05"
	in := "05/07/2026 10:00:00 INFO custom line\n2026-07-05 10:00:01.000 INFO default-format line\n"
	entries, saw, _ := Parse(strings.NewReader(in), layout)
	if !saw || len(entries) != 1 {
		t.Fatalf("entries=%d saw=%v", len(entries), saw)
	}
	if !strings.Contains(entries[0].Msg, "default-format line") {
		t.Error("non-matching lines must become continuations under --ts-format")
	}
	want := time.Date(2026, 7, 5, 10, 0, 0, 0, time.Local)
	if !entries[0].Time.Equal(want) {
		t.Errorf("time = %v, want %v", entries[0].Time, want)
	}
}

func TestParseNoLevelToken(t *testing.T) {
	entries, _, _ := Parse(strings.NewReader("2026-07-05 10:00:00.000 something without a level word here\n"), "")
	if entries[0].Level != "" {
		t.Errorf("level = %q, want empty", entries[0].Level)
	}
}

func TestParseLevelWindowExcludesMessage(t *testing.T) {
	in := "2026-07-05 10:00:00.000  INFO 1 --- [main] c.e.Svc : calling the ERROR handler now\n"
	entries, _, _ := Parse(strings.NewReader(in), "")
	if entries[0].Level != "info" {
		t.Errorf("level = %q, want info (ERROR in the message must not win)", entries[0].Level)
	}
}

func TestParseCRLF(t *testing.T) {
	entries, _, _ := Parse(strings.NewReader("2026-07-05 10:00:00.000 INFO x\r\n"), "")
	if strings.Contains(entries[0].Msg, "\r") {
		t.Error("\\r must be trimmed")
	}
}

func TestParseVeryLongLine(t *testing.T) {
	long := "2026-07-05 10:00:00.000 INFO " + strings.Repeat("x", 100*1024)
	entries, _, err := Parse(strings.NewReader(long+"\n"), "")
	if err != nil || len(entries) != 1 {
		t.Fatalf("long line: entries=%d err=%v", len(entries), err)
	}
}
