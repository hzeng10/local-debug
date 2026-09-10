package devctl

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSingBoxOfficialConfigValidator(t *testing.T) {
	bin := os.Getenv("DEVCTL_SINGBOX_VALIDATOR")
	if bin == "" {
		t.Skip("set DEVCTL_SINGBOX_VALIDATOR to the pinned offline sing-box binary")
	}
	p := Profile{Cluster: Cluster{Domain: "cluster.local"}, Network: Network{RouteCIDRs: []string{"10.96.0.0/12", "10.244.0.0/16"}, BypassCIDRs: []string{"192.0.2.10/32"}, FallbackDNS: "192.0.2.53"}}
	path := filepath.Join(t.TempDir(), "sing-box.json")
	if e := writeJSON(path, SingBoxConfig(p, 1080, 15353)); e != nil {
		t.Fatal(e)
	}
	if b, e := exec.Command(bin, "check", "-c", path).CombinedOutput(); e != nil {
		t.Fatalf("generated config rejected: %v %s", e, b)
	}
}
func TestHelmTemplateUsesExistingNamespace(t *testing.T) {
	bin := os.Getenv("DEVCTL_HELM_VALIDATOR")
	if bin == "" {
		t.Skip("set DEVCTL_HELM_VALIDATOR to offline Helm")
	}
	args := []string{"template", "egress", "../../deploy/gateway", "--namespace", "existing", "--set", "image=internal.example/chisel@sha256:" + strings.Repeat("0", 64), "--set", "egress[0].ports[0].port=53", "--set", "egress[0].ports[0].protocol=TCP", "--set", "telepresence.enabled=true", "--set", "developerGroups[0]=developers", "--set", "readAccess.namespace=business", "--set", "readAccess.deployments[0]=order", "--set", "readAccess.secrets[0]=order-db", "--set", "businessAuthorization.enabled=true", "--set", "businessAuthorization.namespace=business", "--set", "businessAuthorization.selector.app=order", "--set-string", "businessAuthorization.ports[0]=8080"}
	b, e := exec.Command(bin, args...).CombinedOutput()
	if e != nil {
		t.Fatalf("chart rendering: %v %s", e, b)
	}
	for _, bad := range []string{"kind: Namespace", "cluster-admin", "--reverse", "privileged: true"} {
		if strings.Contains(string(b), bad) {
			t.Fatal(bad)
		}
	}
	for _, want := range []string{"kind: Deployment", "kind: NetworkPolicy", "kind: RoleBinding", "kind: AuthorizationPolicy", "namespace: existing", "devctl.io/shared-egress", "telepresence.io/inject-traffic-agent: enabled", `telepresence.io/mount-policies: '{"credentials":"Ignore","/credentials":"Ignore"}'`} {
		if !strings.Contains(string(b), want) {
			t.Fatal(want)
		}
	}
	if _, e := exec.Command(bin, "template", "egress", "../../deploy/gateway", "--namespace", "existing").CombinedOutput(); e == nil {
		t.Fatal("unconfigured chart must fail")
	}
}
