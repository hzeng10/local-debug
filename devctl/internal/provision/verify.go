package provision

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *Admin) readCredentials(ctx context.Context) (string, string, error) {
	m, e := a.get(ctx, a.C.Namespace, "secret", a.C.Credentials.Secret)
	if e != nil {
		return "", "", e
	}
	key, e := base64.StdEncoding.DecodeString(str(at(m, "data", "key.pem")))
	if e != nil {
		return "", "", e
	}
	fp, e := keyFingerprint(key)
	if e != nil {
		return "", "", e
	}
	b, e := base64.StdEncoding.DecodeString(str(at(m, "data", "users.json")))
	if e != nil {
		return "", "", e
	}
	var users map[string][]string
	if e = json.Unmarshal(b, &users); e != nil {
		return "", "", e
	}
	for _, u := range a.C.Credentials.Users {
		for credential := range users {
			if strings.HasPrefix(credential, u+":") {
				return fp, credential, nil
			}
		}
	}
	return "", "", fmt.Errorf("no configured verification user in credential Secret")
}
func (a *Admin) Verify(ctx context.Context) (map[string]any, error) {
	if e := a.C.validate(); e != nil {
		return nil, e
	}
	if len(a.L.Files) == 0 {
		l, e := BundleVerify(a.C.Bundle)
		if e != nil {
			return nil, e
		}
		a.L = l
	}
	if _, e := a.kube(ctx, "-n", a.C.Namespace, "rollout", "status", "deployment/"+a.C.GatewayRelease, "--timeout="+strconv.Itoa(a.C.TimeoutSeconds)+"s"); e != nil {
		return nil, e
	}
	pods, e := a.kube(ctx, "-n", a.C.Namespace, "get", "pods", "-l", "app="+a.C.GatewayRelease, "-o", "json")
	if e != nil {
		return nil, e
	}
	var list map[string]any
	if e = json.Unmarshal(pods, &list); e != nil {
		return nil, e
	}
	ready := 0
	allowedNodes := map[string]bool{}
	for _, n := range a.C.Nodes {
		allowedNodes[n.Name] = true
	}
	for _, v := range arr(list["items"]) {
		p := obj(v)
		if at(p, "metadata", "deletionTimestamp") != nil {
			continue
		}
		if at(p, "status", "phase") != "Running" {
			return nil, fmt.Errorf("gateway Pod is not Running")
		}
		if !allowedNodes[str(at(p, "spec", "nodeName"))] {
			return nil, fmt.Errorf("gateway scheduled outside prepared node inventory")
		}
		if at(p, "metadata", "annotations", "ambient.istio.io/redirection") != "enabled" {
			return nil, fmt.Errorf("gateway lacks actual ambient redirection annotation; inspect Istio CNI enrollment")
		}
		agent := false
		for _, v := range arr(at(p, "spec", "containers")) {
			c := obj(v)
			if c["name"] == "traffic-agent" {
				agent = true
				for _, m := range arr(c["volumeMounts"]) {
					mount := obj(m)
					if mount["name"] == "credentials" || strings.Contains(str(mount["mountPath"]), "credentials") {
						return nil, fmt.Errorf("Traffic Agent exposes credentials volume")
					}
				}
			}
			if e := auditManifests(mustYAMLContainer(c), a.ImageRefs(), map[bool]string{true: "Never", false: "IfNotPresent"}[a.C.ImageMode == "nodes"]); e != nil {
				return nil, e
			}
		}
		if has(a.C.Backends, "telepresence") && !agent {
			return nil, fmt.Errorf("gateway Traffic Agent was not injected")
		}
		for _, v := range arr(at(p, "status", "containerStatuses")) {
			if obj(v)["ready"] != true {
				return nil, fmt.Errorf("gateway container not ready")
			}
		}
		ready++
	}
	if ready == 0 {
		return nil, fmt.Errorf("no gateway Pods")
	}
	if has(a.C.Backends, "telepresence") {
		if e := a.verifyManagerScope(ctx); e != nil {
			return nil, e
		}
		if _, e := a.kube(ctx, "-n", a.C.Namespace, "rollout", "status", "deployment/traffic-manager", "--timeout="+strconv.Itoa(a.C.TimeoutSeconds)+"s"); e != nil {
			return nil, e
		}
	}
	fp, auth, e := a.readCredentials(ctx)
	if e != nil {
		return nil, e
	}
	// Probes traverse the gateway SOCKS connection: a ready Pod alone is insufficient.
	probe := a.probeGateway
	if a.Probe != nil {
		probe = a.Probe
	}
	probes, e := probe(ctx, fp, auth)
	if e != nil {
		return nil, e
	}
	return map[string]any{"ok": true, "gatewayPods": ready, "ambientRedirection": true, "dependencies": probes, "verification": "DNS resolution and TCP connections through the shared gateway; application authentication and business reads require local smoke tests"}, nil
}
func mustYAMLContainer(c map[string]any) []byte { b, _ := json.Marshal(c); return b }
func (a *Admin) verifyManagerScope(ctx context.Context) error {
	b, e := a.helm(ctx, "get", "values", a.C.ManagerRelease, "-n", a.C.Namespace, "-o", "json")
	if e != nil {
		return e
	}
	var m map[string]any
	if e = json.Unmarshal(b, &m); e != nil {
		return e
	}
	if len(arr(m["namespaces"])) != 1 || arr(m["namespaces"])[0] != a.C.Namespace {
		return fmt.Errorf("manager must explicitly manage only kube-system")
	}
	if at(m, "agentInjector", "webhook", "objectSelector", "matchLabels", "devctl.io/shared-egress") != "true" {
		return fmt.Errorf("manager webhook selector is not restricted to shared egress")
	}
	if at(m, "agent", "mountPolicies", "credentials") != "Ignore" || at(m, "agent", "mountPolicies", "/credentials") != "Ignore" {
		return fmt.Errorf("manager credential mount exclusion is absent")
	}
	return nil
}
func freePort() (int, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
func startQuiet(ctx context.Context, args []string, env []string) (func(), error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if e := cmd.Start(); e != nil {
		return nil, e
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	return func() {
		select {
		case <-done:
			return
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	}, nil
}
func waitTCP(ctx context.Context, port int) error {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		c, e := net.DialTimeout("tcp", address("127.0.0.1", port), time.Second)
		if e == nil {
			c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("local verification tunnel startup timed out")
		case <-tick.C:
		}
	}
}
func (a *Admin) probeGateway(ctx context.Context, fp, auth string) ([]map[string]any, error) {
	pf, e := freePort()
	if e != nil {
		return nil, e
	}
	sp, e := freePort()
	if e != nil {
		return nil, e
	}
	args := []string{a.C.Kubectl, "--kubeconfig", a.C.Kubeconfig, "--context", a.C.Context, "-n", a.C.Namespace, "port-forward", "--address=127.0.0.1", "deployment/" + a.C.GatewayRelease, strconv.Itoa(pf) + ":8080"}
	stop, e := startQuiet(ctx, args, nil)
	if e != nil {
		return nil, e
	}
	defer stop()
	if e = waitTCP(ctx, pf); e != nil {
		return nil, e
	}
	stopChisel, e := startQuiet(ctx, []string{tool(a.C.Bundle, "chisel"), "client", "--fingerprint", fp, "http://127.0.0.1:" + strconv.Itoa(pf), "127.0.0.1:" + strconv.Itoa(sp) + ":socks"}, []string{"AUTH=" + auth})
	if e != nil {
		return nil, e
	}
	defer stopChisel()
	if e = waitTCP(ctx, sp); e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, d := range a.C.Dependencies {
		host := d.Service + "." + d.Namespace + ".svc." + a.C.Domain
		c, e := socksDial(ctx, address("127.0.0.1", sp), host, d.Port)
		if e != nil {
			return nil, fmt.Errorf("dependency %s through shared gateway: %w", d.Name, e)
		}
		c.Close()
		out = append(out, map[string]any{"name": d.Name, "dnsAndTCP": true})
	}
	return out, nil
}
func socksDial(ctx context.Context, proxy, host string, port int) (net.Conn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	c, e := d.DialContext(ctx, "tcp", proxy)
	if e != nil {
		return nil, e
	}
	fail := func(e error) (net.Conn, error) { c.Close(); return nil, e }
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, e = c.Write([]byte{5, 1, 0}); e != nil {
		return fail(e)
	}
	b := make([]byte, 2)
	if _, e = io.ReadFull(c, b); e != nil {
		return fail(e)
	}
	if b[0] != 5 || b[1] != 0 {
		return fail(fmt.Errorf("SOCKS handshake rejected"))
	}
	if len(host) > 255 {
		return fail(fmt.Errorf("DNS name too long"))
	}
	req := append([]byte{5, 1, 0, 3, byte(len(host))}, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	if _, e = c.Write(req); e != nil {
		return fail(e)
	}
	b = make([]byte, 4)
	if _, e = io.ReadFull(c, b); e != nil {
		return fail(e)
	}
	if b[0] != 5 || b[1] != 0 {
		return fail(fmt.Errorf("SOCKS connect rejected"))
	}
	n := 0
	switch b[3] {
	case 1:
		n = 4
	case 4:
		n = 16
	case 3:
		b = make([]byte, 1)
		if _, e = io.ReadFull(c, b); e != nil {
			return fail(e)
		}
		n = int(b[0])
	default:
		return fail(fmt.Errorf("invalid SOCKS reply"))
	}
	if _, e = io.ReadFull(c, make([]byte, n+2)); e != nil {
		return fail(e)
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}
func (a *Admin) Export(fp string) error {
	var p map[string]any
	b, e := os.ReadFile(a.C.DeveloperProfile)
	if e != nil {
		return e
	}
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	if p == nil {
		return fmt.Errorf("developer profile must be an object")
	}
	out := filepath.Join(a.C.WorkDir, "handoff")
	if e = secureDir(out); e != nil {
		return e
	}
	cluster := obj(p["cluster"])
	cluster["namespace"] = a.C.Namespace
	cluster["domain"] = a.C.Domain
	cluster["context"] = a.C.DeveloperContext
	if a.C.DeveloperContext == "" {
		cluster["context"] = a.C.Context
	}
	cluster["sshHost"] = a.C.DeveloperSSHHost
	if a.C.DeveloperSSHHost == "" {
		cluster["sshHost"] = "REPLACE_WITH_DEVELOPER_SSH_ALIAS"
	}
	p["cluster"] = cluster
	source := obj(p["source"])
	source["deployment"] = a.C.BusinessDeployment
	source["container"] = a.C.BusinessContainer
	p["source"] = source
	network := obj(p["network"])
	network["managerNamespace"] = a.C.Namespace
	network["gatewayNamespace"] = a.C.Namespace
	network["gatewayDeployment"] = a.C.GatewayRelease
	network["fingerprint"] = fp
	network["clusterDNS"] = a.C.ClusterDNS
	network["routeCIDRs"] = a.C.RouteCIDRs
	network["bypassCIDRs"] = a.C.BypassCIDRs
	network["fallbackDNS"] = a.C.FallbackDNS
	p["network"] = network
	deps := []map[string]any{}
	for _, d := range a.C.Dependencies {
		deps = append(deps, map[string]any{"name": d.Name, "address": address(d.Service+"."+d.Namespace+".svc."+a.C.Domain, d.Port)})
	}
	p["dependencies"] = deps
	network["backend"] = "gateway"
	network["transport"] = "ssh"
	if e = writeJSON(filepath.Join(out, "gde-adapter.ssh.json"), p); e != nil {
		return e
	}
	if has(a.C.Backends, "telepresence") {
		network["backend"] = "telepresence"
		network["transport"] = "kubectl"
		if e = writeJSON(filepath.Join(out, "gde-adapter.kubectl.json"), p); e != nil {
			return e
		}
	}
	return nil
}
