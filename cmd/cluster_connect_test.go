package cmd

import (
	"strings"
	"testing"
)

func TestProbeHasFail(t *testing.T) {
	cases := []struct {
		name   string
		stages []probeStage
		want   bool
	}{
		{"empty", nil, false},
		{"all pass", []probeStage{{Status: "pass"}, {Status: "pass"}}, false},
		{"warn only", []probeStage{{Status: "pass"}, {Status: "warn"}}, false},
		{"has fail", []probeStage{{Status: "pass"}, {Status: "fail"}, {Status: "warn"}}, true},
	}
	for _, c := range cases {
		if got := probeHasFail(c.stages); got != c.want {
			t.Errorf("%s: probeHasFail = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRenderProbe(t *testing.T) {
	// A proxy bridge: REST passes but port-forward fails — the signature case the
	// command exists to surface. The hint must render under the failing stage only.
	r := clusterProbeResult{
		Stages: []probeStage{
			{Name: "api", Status: "pass", Detail: "kubernetes v1.31 reachable"},
			{Name: "port-forward", Status: "fail", Detail: "upgrade dropped", Hint: "use ssh -L"},
		},
		OK: false,
	}
	out := renderProbe(r)

	if !strings.Contains(out, "✓ api") {
		t.Errorf("passing stage not rendered with ✓: %q", out)
	}
	if !strings.Contains(out, "✗ port-forward") {
		t.Errorf("failing stage not rendered with ✗: %q", out)
	}
	if !strings.Contains(out, "↳ use ssh -L") {
		t.Errorf("hint not rendered under failing stage: %q", out)
	}
	if !strings.HasSuffix(out, "overall: ISSUES") {
		t.Errorf("overall line missing/incorrect: %q", out)
	}
	// A passing stage must not print a hint line even if one were set.
	if strings.Contains(out, "kubernetes v1.31 reachable\n    ↳") {
		t.Errorf("hint rendered under a passing stage: %q", out)
	}
}

func TestMinimalKubeconfig(t *testing.T) {
	http := minimalKubeconfig("http://127.0.0.1:8001", "")
	if !strings.Contains(http, "server: http://127.0.0.1:8001") {
		t.Errorf("server not set: %q", http)
	}
	if !strings.Contains(http, "name: ldbg-proxy") {
		t.Errorf("default context name not applied: %q", http)
	}
	if strings.Contains(http, "insecure-skip-tls-verify") {
		t.Errorf("http endpoint should not add TLS skip: %q", http)
	}
	if !strings.Contains(http, "current-context: ldbg-proxy") {
		t.Errorf("current-context missing: %q", http)
	}

	https := minimalKubeconfig("https://127.0.0.1:6443", "bastion")
	if !strings.Contains(https, "insecure-skip-tls-verify: true") {
		t.Errorf("https endpoint should add TLS skip: %q", https)
	}
	if !strings.Contains(https, "name: bastion") {
		t.Errorf("custom context name not applied: %q", https)
	}
}
