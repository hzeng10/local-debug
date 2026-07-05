// vendored from github.com/hzeng10/log-analysis cli/internal/logsql @v0.1.1

package logsql

import "testing"

func TestBuild(t *testing.T) {
	cases := []struct {
		name string
		f    Filter
		want string
	}{
		{
			name: "keyword only searches all collected services",
			f:    Filter{Since: "1h", Query: "Exception"},
			want: `_time:1h "Exception"`,
		},
		{
			name: "structured filters AND together",
			f:    Filter{Since: "1h", Namespace: "prod", Service: "payment", Query: "error"},
			want: `_time:1h namespace:="prod" service:="payment" "error"`,
		},
		{
			name: "case-insensitive wraps in i()",
			f:    Filter{Since: "1h", Query: "Exception", IgnoreCase: true},
			want: `_time:1h i("Exception")`,
		},
		{
			name: "multiple keywords AND in a group",
			f:    Filter{Since: "30m", Query: "NullPointer timeout"},
			want: `_time:30m ("NullPointer" "timeout")`,
		},
		{
			name: "quoted phrase stays together",
			f:    Filter{Since: "1h", Query: `"connection refused"`},
			want: `_time:1h "connection refused"`,
		},
		{
			name: "field syntax is treated as raw LogsQL",
			f:    Filter{Since: "1h", Query: "trace_id:abc123"},
			want: `_time:1h (trace_id:abc123)`,
		},
		{
			name: "level is lowercased and exact-matched",
			f:    Filter{Since: "1d", Level: "ERROR"},
			want: `_time:1d level:="error"`,
		},
		{
			name: "month range converts to days",
			f:    Filter{Since: "1mo"},
			want: `_time:30d`,
		},
		{
			name: "absolute range",
			f:    Filter{From: "2026-06-01T00:00:00Z", To: "2026-06-02T00:00:00Z"},
			want: `_time:[2026-06-01T00:00:00Z, 2026-06-02T00:00:00Z]`,
		},
		{
			name: "no filters matches everything",
			f:    Filter{From: "", To: "", Since: "5m"},
			want: `_time:5m`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, err := tc.f.Build()
			if err != nil {
				t.Fatalf("Build() error: %v", err)
			}
			if got != tc.want {
				t.Errorf("Build()\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
}

func TestNormalizeSince(t *testing.T) {
	ok := map[string]string{
		"": "1h", "30m": "30m", "1h": "1h", "12h": "12h",
		"1d": "1d", "1w": "1w", "1mo": "30d", "2months": "60d", "1y": "1y",
	}
	for in, want := range ok {
		got, err := NormalizeSince(in)
		if err != nil {
			t.Errorf("NormalizeSince(%q) unexpected error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeSince(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"abc", "1", "h", "-1h", "1mo5d"} {
		if _, err := NormalizeSince(bad); err == nil {
			t.Errorf("NormalizeSince(%q) expected error", bad)
		}
	}
}

func TestBuildLiveOmitsTime(t *testing.T) {
	f := Filter{Since: "1h", Service: "payment", Query: "error"}
	got, err := f.BuildLive()
	if err != nil {
		t.Fatal(err)
	}
	want := `service:="payment" "error"`
	if got != want {
		t.Errorf("BuildLive()\n got: %s\nwant: %s", got, want)
	}
}
