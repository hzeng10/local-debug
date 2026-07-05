// Local addition (NOT vendored): tests for the render contract ldbg depends on.
package format

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hzeng10/local-debug/internal/logquery/logsql"
)

func sampleEnv() Envelope {
	return Envelope{
		Query: `_time:1h service:="orders"`,
		Range: logsql.Range{Since: "1h"},
		Count: 2,
		Logs: []map[string]any{
			{"_time": "t1", "level": "info", "service": "orders", "_msg": "line one", "pod": "p1"},
			{"_time": "t2", "level": "error", "service": "orders", "_msg": "line\ntwo", "pod": "p2"},
		},
	}
}

func TestWriteEnvelopeModes(t *testing.T) {
	var buf bytes.Buffer

	// json: a single document with the envelope keys.
	if err := WriteEnvelope(&buf, "json", sampleEnv(), nil); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("json mode must emit valid JSON: %v", err)
	}
	for _, k := range []string{"query", "range", "count", "truncated", "logs"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("json envelope missing key %q", k)
		}
	}

	// jsonl: one record per line.
	buf.Reset()
	if err := WriteEnvelope(&buf, "jsonl", sampleEnv(), nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("jsonl want 2 lines, got %d", len(lines))
	}
	for _, l := range lines {
		if !json.Valid([]byte(l)) {
			t.Errorf("jsonl line not valid JSON: %s", l)
		}
	}

	// raw: message text only.
	buf.Reset()
	if err := WriteEnvelope(&buf, "raw", sampleEnv(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(buf.String(), "line one\n") {
		t.Errorf("raw mode wrong: %q", buf.String())
	}

	// table: header + newline-folded messages.
	buf.Reset()
	if err := WriteEnvelope(&buf, "table", sampleEnv(), nil); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, "TIME") || !strings.Contains(s, "MESSAGE") {
		t.Errorf("table header missing: %q", s)
	}
	if !strings.Contains(s, "line two") {
		t.Errorf("table must fold multi-line messages: %q", s)
	}

	// unknown mode errors.
	if err := WriteEnvelope(&buf, "xml", sampleEnv(), nil); err == nil {
		t.Error("unknown mode should error")
	}
}

func TestProjected(t *testing.T) {
	env := Projected(sampleEnv(), []string{"service", "_msg"})
	if len(env.Logs) != 2 {
		t.Fatalf("want 2 logs, got %d", len(env.Logs))
	}
	for _, l := range env.Logs {
		if len(l) != 2 {
			t.Errorf("projection should keep exactly 2 fields, got %v", l)
		}
		if _, ok := l["pod"]; ok {
			t.Errorf("pod should have been projected away: %v", l)
		}
	}
	// no-op without fields, and the original envelope must stay intact.
	orig := sampleEnv()
	if got := Projected(orig, nil); len(got.Logs[0]) != len(orig.Logs[0]) {
		t.Error("empty projection must be a no-op")
	}
	if len(orig.Logs[0]) != 5 {
		t.Errorf("original envelope mutated: %v", orig.Logs[0])
	}
}
