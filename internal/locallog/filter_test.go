package locallog

import (
	"testing"
	"time"
)

func TestParseSince(t *testing.T) {
	ok := map[string]time.Duration{
		"45s": 45 * time.Second,
		"30m": 30 * time.Minute,
		"4h":  4 * time.Hour,
		"2d":  48 * time.Hour,
		"1w":  7 * 24 * time.Hour,
		"1y":  365 * 24 * time.Hour,
		"1mo": 720 * time.Hour, // month → 30d upstream
		"":    time.Hour,       // NormalizeSince's default — callers guard for "unset"
	}
	for in, want := range ok {
		got, err := ParseSince(in)
		if err != nil || got != want {
			t.Errorf("ParseSince(%q) = (%v, %v), want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"5x", "abc", "-1h"} {
		if _, err := ParseSince(bad); err == nil {
			t.Errorf("ParseSince(%q) expected error", bad)
		}
	}
}

func TestSplitTermsQuotedPhrase(t *testing.T) {
	got := splitTerms(`a "b c" d`)
	if len(got) != 3 || got[0] != "a" || got[1] != "b c" || got[2] != "d" {
		t.Errorf("splitTerms = %v", got)
	}
}

func ts(h, m int) time.Time { return time.Date(2026, 7, 5, h, m, 0, 0, time.Local) }

func sampleEntries() []Entry {
	return []Entry{
		{Msg: "banner only"}, // HasTime=false
		{Time: ts(8, 0), HasTime: true, Level: "info", Msg: "old info line"},
		{Time: ts(9, 55), HasTime: true, Level: "error", Msg: "boom NullPointerException\n\tat com.example.Svc.handle(Svc.java:42)"},
		{Time: ts(9, 58), HasTime: true, Level: "info", Msg: "Recovered fine"},
	}
}

func TestApplyKeywordAND(t *testing.T) {
	f := Filter{Query: "boom NullPointer"}
	got := f.Apply(sampleEntries(), true)
	if len(got) != 1 || got[0].Level != "error" {
		t.Errorf("AND terms: got %v", got)
	}
}

func TestApplyKeywordMatchesContinuation(t *testing.T) {
	f := Filter{Query: "Svc.java:42"}
	if got := f.Apply(sampleEntries(), true); len(got) != 1 {
		t.Errorf("stack-frame grep failed: %v", got)
	}
}

func TestApplyIgnoreCase(t *testing.T) {
	if got := (Filter{Query: "nullpointerexception", IgnoreCase: true}).Apply(sampleEntries(), true); len(got) != 1 {
		t.Errorf("-i failed: %v", got)
	}
	if got := (Filter{Query: "nullpointerexception"}).Apply(sampleEntries(), true); len(got) != 0 {
		t.Errorf("case-sensitive default violated: %v", got)
	}
}

func TestApplyLevel(t *testing.T) {
	got := (Filter{Level: "ERROR"}).Apply(sampleEntries(), true)
	if len(got) != 1 || got[0].Level != "error" {
		t.Errorf("level filter: %v", got)
	}
	// banner (empty level) must be excluded under a level filter
	for _, e := range got {
		if e.Level == "" {
			t.Error("empty-level entry passed a level filter")
		}
	}
}

func TestApplySinceWithInjectedNow(t *testing.T) {
	f := Filter{Since: time.Hour, Now: func() time.Time { return ts(10, 0) }}
	got := f.Apply(sampleEntries(), true)
	if len(got) != 2 {
		t.Fatalf("since 1h from 10:00 should keep 2 entries, got %d", len(got))
	}
	for _, e := range got {
		if !e.HasTime {
			t.Error("HasTime=false entry must be excluded under a time filter")
		}
	}
}

func TestApplyFromTo(t *testing.T) {
	f := Filter{From: ts(9, 50), To: ts(9, 56)}
	got := f.Apply(sampleEntries(), true)
	if len(got) != 1 || got[0].Level != "error" {
		t.Errorf("from/to window: %v", got)
	}
}

func TestApplyNoTimestampsDisablesTimeFilters(t *testing.T) {
	f := Filter{Since: time.Minute, Now: func() time.Time { return ts(23, 0) }}
	got := f.Apply(sampleEntries(), false) // sawTimestamps=false
	if len(got) != len(sampleEntries()) {
		t.Errorf("time filters must be a no-op without timestamps, got %d", len(got))
	}
}

func TestTail(t *testing.T) {
	e := sampleEntries()
	kept, trunc := Tail(e, 2)
	if !trunc || len(kept) != 2 || kept[0].Level != "error" {
		t.Errorf("Tail(2): kept=%v trunc=%v", kept, trunc)
	}
	kept, trunc = Tail(e, 0)
	if trunc || len(kept) != len(e) {
		t.Errorf("Tail(0) must keep all: %d", len(kept))
	}
}
