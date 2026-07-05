package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hzeng10/local-debug/internal/logquery/logsql"
	"github.com/hzeng10/local-debug/internal/tp"
)

func TestVLResolverPrecedence(t *testing.T) {
	pfOK := func(ctx context.Context) (string, func(), error) {
		return "http://127.0.0.1:55555", func() {}, nil
	}
	pfFail := func(ctx context.Context) (string, func(), error) {
		return "", nil, errors.New("no ready pod")
	}
	probeYes := func(string) bool { return true }
	probeNo := func(string) bool { return false }

	cases := []struct {
		name       string
		r          vlResolver
		wantAddr   string
		wantSource string
		wantErr    bool
	}{
		{
			name:       "flag wins over everything",
			r:          vlResolver{flagAddr: "http://flag:9428", envAddr: "http://env:9428", connected: true, probe: probeYes, portFwd: pfOK},
			wantAddr:   "http://flag:9428",
			wantSource: "flag",
		},
		{
			name:       "env when no flag",
			r:          vlResolver{envAddr: "http://env:9428", connected: true, probe: probeYes, portFwd: pfOK},
			wantAddr:   "http://env:9428",
			wantSource: "env",
		},
		{
			name:       "whitespace env is treated as unset",
			r:          vlResolver{envAddr: "   ", namespace: "logging", connected: true, probe: probeYes, portFwd: pfOK},
			wantAddr:   "http://victorialogs.logging.svc.cluster.local:9428",
			wantSource: "tunnel",
		},
		{
			name:       "connected and probe ok uses tunnel FQDN",
			r:          vlResolver{namespace: "logging", connected: true, probe: probeYes, portFwd: pfOK},
			wantAddr:   "http://victorialogs.logging.svc.cluster.local:9428",
			wantSource: "tunnel",
		},
		{
			name: "connected but probe fails falls through to port-forward",
			r: vlResolver{namespace: "logging", connected: true,
				probe:   func(a string) bool { return strings.HasPrefix(a, "http://127.") },
				portFwd: pfOK},
			wantAddr:   "http://127.0.0.1:55555",
			wantSource: "port-forward",
		},
		{
			name:       "disconnected goes straight to port-forward",
			r:          vlResolver{namespace: "logging", connected: false, probe: probeYes, portFwd: pfOK},
			wantAddr:   "http://127.0.0.1:55555",
			wantSource: "port-forward",
		},
		{
			name:    "everything failing errors",
			r:       vlResolver{namespace: "logging", connected: false, probe: probeNo, portFwd: pfFail},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, cleanup, source, err := tc.r.resolve(context.Background())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got addr=%q source=%q", addr, source)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cleanup != nil {
				cleanup()
			}
			if addr != tc.wantAddr || source != tc.wantSource {
				t.Errorf("resolve = (%q, %q), want (%q, %q)", addr, source, tc.wantAddr, tc.wantSource)
			}
		})
	}
}

func TestApplyLogDefaults(t *testing.T) {
	oneIntercept := &tp.Status{Connected: true, Intercepts: []tp.InterceptInfo{{Name: "orders", Namespace: "demo"}}}
	twoIntercepts := &tp.Status{Connected: true, Intercepts: []tp.InterceptInfo{{Name: "a", Namespace: "x"}, {Name: "b", Namespace: "y"}}}

	cases := []struct {
		name        string
		f           logsql.Filter
		st          *tp.Status
		nsFlag      string
		wantService string
		wantNS      string
	}{
		{"one intercept defaults service and namespace", logsql.Filter{}, oneIntercept, "", "orders", "demo"},
		{"explicit -n beats intercept namespace", logsql.Filter{}, oneIntercept, "prod", "orders", "prod"},
		{"explicit service disables namespace default", logsql.Filter{Service: "billing"}, oneIntercept, "", "billing", ""},
		{"two intercepts default nothing", logsql.Filter{}, twoIntercepts, "", "", ""},
		{"nil status defaults nothing", logsql.Filter{}, nil, "", "", ""},
		{"nil status still honors -n", logsql.Filter{}, nil, "demo", "", "demo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			applyLogDefaults(&f, tc.st, tc.nsFlag)
			if f.Service != tc.wantService || f.Namespace != tc.wantNS {
				t.Errorf("got (service=%q ns=%q), want (service=%q ns=%q)", f.Service, f.Namespace, tc.wantService, tc.wantNS)
			}
		})
	}
}

func TestQueryHint(t *testing.T) {
	intercepted := &tp.Status{Intercepts: []tp.InterceptInfo{{Name: "orders", Namespace: "demo"}}}

	if h := queryHint(false, "orders", nil); h != "" {
		t.Errorf("no signals should mean no hint, got %q", h)
	}
	if h := queryHint(true, "", nil); !strings.Contains(h, "--limit") {
		t.Errorf("truncated hint missing, got %q", h)
	}
	if h := queryHint(false, "orders", intercepted); !strings.Contains(h, "intercepted") {
		t.Errorf("intercept hint missing, got %q", h)
	}
	if h := queryHint(false, "billing", intercepted); h != "" {
		t.Errorf("non-intercepted service should not hint, got %q", h)
	}
	both := queryHint(true, "orders", intercepted)
	if !strings.Contains(both, "; ") || !strings.Contains(both, "--limit") || !strings.Contains(both, "intercepted") {
		t.Errorf("combined hint malformed: %q", both)
	}
}

func TestPositionalService(t *testing.T) {
	v := vlFlags{}
	if err := v.positionalService([]string{"orders"}); err != nil || v.service != "orders" {
		t.Fatalf("positional should set service, got %q err=%v", v.service, err)
	}
	v = vlFlags{service: "orders"}
	if err := v.positionalService([]string{"orders"}); err != nil {
		t.Fatalf("same name twice is fine, got %v", err)
	}
	v = vlFlags{service: "billing"}
	if err := v.positionalService([]string{"orders"}); err == nil {
		t.Fatal("conflicting names should error")
	}
}
