package devctl

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func probeHTTP(ctx context.Context, url string, want int) error {
	if want == 0 {
		want = 200
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP probe failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != want {
		return fmt.Errorf("HTTP status %d, expected %d", resp.StatusCode, want)
	}
	return nil
}
func probeTCP(ctx context.Context, address string) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, e := d.DialContext(ctx, "tcp", address)
	if e != nil {
		return fmt.Errorf("TCP connection failed")
	}
	c.Close()
	return nil
}
func probeDependencies(ctx context.Context, p Profile) []Check {
	out := []Check{}
	for _, d := range p.Dependencies {
		var err error
		if d.URL != "" {
			err = probeHTTP(ctx, d.URL, d.Status)
		} else {
			err = probeTCP(ctx, d.Address)
		}
		detail := "reachable (transport check; run application tests for protocol semantics)"
		if err != nil {
			detail = err.Error()
		}
		out = append(out, Check{Name: d.Name, OK: err == nil, Detail: detail})
	}
	return out
}
func toolsRequired(p Profile) []string {
	a := []string{p.Application.Java}
	if p.Network.Transport == "ssh" {
		a = append(a, p.Network.SSH)
	} else {
		a = append(a, p.Cluster.Kubectl)
	}
	if p.Network.Backend == "telepresence" {
		a = append(a, p.Network.Telepresence)
	} else {
		a = append(a, p.Network.Chisel, p.Network.SingBox)
	}
	return a
}
func routeConflicts(p Profile) error {
	if p.Network.Backend != "gateway" {
		return nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, iface := range ifaces {
		if strings.HasPrefix(iface.Name, "devctl") {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, a := range addresses {
			local, e := netip.ParsePrefix(a.String())
			if e != nil || !local.Addr().Is4() {
				continue
			}
			for _, route := range p.Network.RouteCIDRs {
				r, _ := netip.ParsePrefix(route)
				if local.Overlaps(r) {
					return fmt.Errorf("cluster route %s overlaps interface %s (%s)", route, iface.Name, local)
				}
			}
		}
	}
	return nil
}
func Doctor(ctx context.Context, p Profile) []Check {
	out := []Check{}
	for _, tool := range toolsRequired(p) {
		_, err := exec.LookPath(tool)
		detail := "installed"
		if err != nil {
			detail = "not found on PATH"
		}
		out = append(out, Check{Name: tool, OK: err == nil, Detail: detail})
	}
	cmd := exec.CommandContext(ctx, p.Application.Java, "-version")
	b, err := cmd.CombinedOutput()
	ok := err == nil && (strings.Contains(string(b), `version "21`) || strings.Contains(string(b), "openjdk 21"))
	out = append(out, Check{Name: "JDK 21", OK: ok, Detail: "requires a locally installed JDK 21"})
	err = routeConflicts(p)
	detail := "no overlap with local interface prefixes; corporate VPN route tables still require acceptance testing"
	if err != nil {
		detail = err.Error()
	}
	out = append(out, Check{Name: "route overlap", OK: err == nil, Detail: detail})
	var dep Deployment
	err = (Kube{p}).Get(ctx, p.Cluster.Namespace, "deployment", p.Source.Deployment, &dep)
	detail = "source workload readable"
	if err != nil {
		detail = err.Error()
	}
	out = append(out, Check{Name: "Kubernetes read", OK: err == nil, Detail: detail})
	if err == nil {
		dir, e := os.MkdirTemp("", "devctl-doctor-")
		if e == nil {
			_, e = Prepare(ctx, p, Kube{p}, dir)
			_ = os.RemoveAll(dir)
		}
		detail := "allowlisted environment and file mappings resolved"
		if e != nil {
			detail = e.Error()
		}
		out = append(out, Check{Name: "startup configuration", OK: e == nil, Detail: detail})
	}
	if p.Network.Backend == "gateway" {
		var gate Deployment
		err = (Kube{p}).Get(ctx, p.Network.GatewayNamespace, "deployment", p.Network.GatewayDeployment, &gate)
		detail = "preinstalled gateway readable"
		if err != nil {
			detail = err.Error()
		}
		out = append(out, Check{Name: "shared gateway", OK: err == nil, Detail: detail})
	}
	return out
}
