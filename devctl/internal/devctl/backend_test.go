package devctl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteCommandQuoting(t *testing.T) {
	s := remoteCommand([]string{"kubectl", "--context", "a'; $(touch /tmp/pwn); '"})
	if s != "exec 'kubectl' '--context' 'a'\"'\"'; $(touch /tmp/pwn); '\"'\"''" {
		t.Fatal(s)
	}
}
func TestSSHContainsNoLocalKubeconfigOrCredentials(t *testing.T) {
	p := Profile{Cluster: Cluster{Context: "test", Namespace: "business", RemoteKubectl: "kubectl", SSHHost: "node"}, Network: Network{Transport: "ssh", SSH: "ssh", GatewayNamespace: "existing", GatewayDeployment: "egress", GatewayPort: 8080}}
	for _, a := range [][]string{(Kube{p}).Command("get", "deployment", "order", "-o", "json"), ForwardCommand(p, 32123)} {
		s := strings.Join(a, " ")
		if strings.Contains(s, "kubeconfig") || strings.Contains(s, "--token") {
			t.Fatal(s)
		}
		if !strings.Contains(s, "exec 'kubectl'") || !strings.Contains(s, "-- node") {
			t.Fatal(s)
		}
	}
}
func TestGeneratedNetworkHasNoDefaultRouteOrReverseTunnel(t *testing.T) {
	p := Profile{Cluster: Cluster{Domain: "cluster.local"}, Network: Network{RouteCIDRs: []string{"10.96.0.0/12"}, BypassCIDRs: []string{"192.0.2.1/32"}, FallbackDNS: "192.0.2.53"}}
	b, _ := json.Marshal(SingBoxConfig(p, 1080, 15353))
	if strings.Contains(string(b), "0.0.0.0/0") {
		t.Fatal(string(b))
	}
	for _, s := range []string{"cluster-dns", "corporate-dns", "route_exclude_address", "hijack-dns", "reject"} {
		if !strings.Contains(string(b), s) {
			t.Fatal(s)
		}
	}
}
func TestTPStatusStrictParsing(t *testing.T) {
	for _, b := range []string{`{"user_daemon":{"status":"Disconnected"}}`, `not JSON`, `{"error":"Connected elsewhere"}`} {
		if tpConnected([]byte(b)) {
			t.Fatal(b)
		}
	}
	if !tpConnected([]byte(`{"user_daemon":{"status":"Connected"}}`)) {
		t.Fatal("connected status rejected")
	}
}
func TestHealthRejectsRedirectAndUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/login", 302) }))
	defer srv.Close()
	if probeHTTP(context.Background(), srv.URL, 200) == nil {
		t.Fatal("redirect is not ready")
	}
}
func TestControlRequiresPostAndToken(t *testing.T) {
	s := &supervisor{token: "private"}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	resp, e := http.Get(srv.URL + "/status")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 {
		t.Fatal(resp.StatusCode)
	}
	resp, e = http.Post(srv.URL+"/disconnect", "application/json", strings.NewReader(`{}`))
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal(resp.StatusCode)
	}
}
func TestProfileRejectsUnknownFieldsAndUnreviewedTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")
	_ = os.WriteFile(path, []byte(`{"version":1,"unknown":true}`), 0600)
	if _, e := LoadProfile(path, "", ""); e == nil {
		t.Fatal("unknown property accepted")
	}
}
