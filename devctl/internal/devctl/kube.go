package devctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

type Reader interface {
	Get(context.Context, string, string, string, any) error
}
type Kube struct{ Profile Profile }

// QuotePOSIX quotes each argument independently. JSON encoding is not shell escaping.
func QuotePOSIX(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func remoteCommand(argv []string) string {
	a := make([]string, len(argv))
	for i, v := range argv {
		a[i] = QuotePOSIX(v)
	}
	return "exec " + strings.Join(a, " ")
}
func (k Kube) Command(args ...string) []string {
	p := k.Profile
	base := []string{p.Cluster.Kubectl, "--context", p.Cluster.Context}
	if p.Network.Transport == "ssh" {
		base[0] = p.Cluster.RemoteKubectl
		return []string{p.Network.SSH, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", p.Cluster.SSHHost, remoteCommand(append(base, args...))}
	}
	return append(base, args...)
}
func (k Kube) Get(ctx context.Context, ns, kind, name string, out any) error {
	if !identifier.MatchString(ns) || !identifier.MatchString(name) {
		return fmt.Errorf("invalid Kubernetes resource reference")
	}
	switch kind {
	case "deployment", "configmap", "secret":
	default:
		return fmt.Errorf("read of resource kind %q is not supported", kind)
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	a := k.Command("--request-timeout=20s", "-n", ns, "get", kind, name, "-o", "json")
	cmd := exec.CommandContext(ctx, a[0], a[1:]...)
	b, err := cmd.Output()
	// Never include kubectl stdout/stderr: it can contain credential material.
	if err != nil {
		return fmt.Errorf("cannot read %s/%s in %s; check context, connectivity and named-resource RBAC", kind, name, ns)
	}
	if err = json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("invalid JSON reading %s/%s", kind, name)
	}
	return nil
}
