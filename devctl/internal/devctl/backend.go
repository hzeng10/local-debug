package devctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Link struct {
	cleanupError string
	mu           sync.Mutex
	state        string
	message      string
	cancel       context.CancelFunc
	done         chan struct{}
	p            Profile
	dir          string
	children     []*Process
	tpOwned      bool
	dnsAddress   string
}

func (l *Link) State() (string, string) { l.mu.Lock(); defer l.mu.Unlock(); return l.state, l.message }
func (l *Link) set(state, message string) {
	l.mu.Lock()
	l.state = state
	l.message = message
	l.mu.Unlock()
}
func StartLink(parent context.Context, p Profile, dir string) (*Link, error) {
	ctx, cancel := context.WithCancel(parent)
	l := &Link{p: p, dir: dir, cancel: cancel, done: make(chan struct{}), state: "connecting"}
	if err := secureDir(dir); err != nil {
		cancel()
		return nil, err
	}
	ready := make(chan error, 1)
	go func() {
		defer close(l.done)
		defer l.cleanup()
		first := true
		for {
			if ctx.Err() != nil {
				return
			}
			l.set("connecting", "")
			err := l.connect(ctx)
			if err != nil {
				l.cleanup()
				l.set("degraded", err.Error())
				if first {
					ready <- err
					return
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
					continue
				}
			}
			l.set("connected", "")
			if first {
				ready <- nil
				first = false
			}
			for ctx.Err() == nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(3 * time.Second):
				}
				if !l.healthy(ctx) {
					l.set("degraded", "network backend stopped; reconnecting")
					l.cleanup()
					break
				}
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			cancel()
			<-l.done
			return nil, err
		}
		return l, nil
	case <-parent.Done():
		cancel()
		<-l.done
		return nil, parent.Err()
	}
}
func (l *Link) Close() error {
	l.cancel()
	<-l.done
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cleanupError != "" {
		return fmt.Errorf("%s", l.cleanupError)
	}
	return nil
}
func (l *Link) tp(args ...string) []string {
	return append([]string{l.p.Network.Telepresence, "--config", filepath.Join(l.dir, "telepresence.yml")}, args...)
}
func tpConnected(b []byte) bool {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(x any) bool {
		switch t := x.(type) {
		case map[string]any:
			for k, v := range t {
				if strings.EqualFold(k, "status") {
					if s, ok := v.(string); ok && strings.EqualFold(s, "connected") {
						return true
					}
				}
				if visit(v) {
					return true
				}
			}
		case []any:
			for _, v := range t {
				if visit(v) {
					return true
				}
			}
		}
		return false
	}
	return visit(v)
}
func (l *Link) connect(ctx context.Context) error {
	p := l.p
	if p.Network.Backend == "telepresence" {
		if err := writePrivate(filepath.Join(l.dir, "telepresence.yml"), []byte("usage:\n  enabled: false\nintercept:\n  localShortcut: false\n")); err != nil {
			return err
		}
		limited, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		b, e := runQuiet(limited, l.tp("status", "--format", "json"))
		if e == nil && tpConnected(b) {
			return fmt.Errorf("an existing Telepresence connection is active; disconnect it before devctl takes ownership")
		}
		_, err := runQuiet(limited, l.tp("connect", "--context", p.Cluster.Context, "--namespace", p.Network.GatewayNamespace, "--manager-namespace", p.Network.ManagerNamespace, "--proxy-via", "all="+p.Network.GatewayDeployment, "--format", "json"))
		if err != nil {
			return fmt.Errorf("Telepresence connect failed; verify the preinstalled manager/agent and API streaming access")
		}
		l.tpOwned = true
		return retryUntil(limited, time.Second, func() error {
			b, e := runQuiet(limited, l.tp("status", "--format", "json"))
			if e != nil || !tpConnected(b) {
				return fmt.Errorf("Telepresence not connected")
			}
			return nil
		})
	}
	if err := routeConflicts(p); err != nil {
		return err
	}
	auth := os.Getenv(p.Network.AuthEnv)
	if auth == "" {
		return fmt.Errorf("set gateway authentication environment variable %s", p.Network.AuthEnv)
	}
	pf, e := freePort()
	if e != nil {
		return e
	}
	socks, e := freePort()
	if e != nil {
		return e
	}
	dns, e := freePort()
	if e != nil {
		return e
	}
	if e = l.child(ForwardCommand(p, pf), nil, "forward", nil); e != nil {
		return e
	}
	limited, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if e = retryUntil(limited, 250*time.Millisecond, func() error { return probeTCP(limited, fmt.Sprintf("127.0.0.1:%d", pf)) }); e != nil {
		return fmt.Errorf("Kubernetes/SSH port-forward did not become ready")
	}
	env := append(os.Environ(), "AUTH="+auth)
	argv := []string{p.Network.Chisel, "client", "--fingerprint", p.Network.Fingerprint, "--keepalive", "10s", "http://127.0.0.1:" + strconv.Itoa(pf), fmt.Sprintf("127.0.0.1:%d:socks", socks), fmt.Sprintf("127.0.0.1:%d:%s:53", dns, p.Network.ClusterDNS)}
	if e = l.child(argv, env, "chisel", []string{auth}); e != nil {
		return e
	}
	if e = retryUntil(limited, 250*time.Millisecond, func() error {
		if e := probeTCP(limited, fmt.Sprintf("127.0.0.1:%d", socks)); e != nil {
			return e
		}
		return probeTCP(limited, fmt.Sprintf("127.0.0.1:%d", dns))
	}); e != nil {
		return fmt.Errorf("authenticated gateway tunnel did not become ready")
	}
	l.dnsAddress = fmt.Sprintf("127.0.0.1:%d", dns)
	if e = probeDNS(limited, l.dnsAddress, "kubernetes.default.svc."+p.Cluster.Domain); e != nil {
		return e
	}
	configPath := filepath.Join(l.dir, "sing-box.json")
	if e = writeJSON(configPath, SingBoxConfig(p, socks, dns)); e != nil {
		return e
	}
	if _, e = runQuiet(limited, []string{p.Network.SingBox, "check", "-c", configPath}); e != nil {
		return fmt.Errorf("sing-box configuration rejected; use the documented 1.12 release family")
	}
	if e = l.child([]string{p.Network.SingBox, "run", "-c", configPath}, nil, "tun", nil); e != nil {
		return e
	}
	return retryUntil(limited, 500*time.Millisecond, func() error {
		for _, child := range l.children {
			if !child.Running() {
				return fmt.Errorf("network process exited; inspect private transport logs")
			}
		}
		addresses, e := net.DefaultResolver.LookupHost(limited, "kubernetes.default.svc."+p.Cluster.Domain)
		if e != nil || len(addresses) == 0 {
			return fmt.Errorf("cluster DNS is not ready")
		}
		return nil
	})
}
func (l *Link) child(argv, env []string, name string, secrets []string) error {
	p, err := StartProcess(argv, "", env, filepath.Join(l.dir, name+".log"), secrets)
	if err != nil {
		return err
	}
	l.children = append(l.children, p)
	return nil
}
func (l *Link) healthy(ctx context.Context) bool {
	if l.p.Network.Backend == "telepresence" {
		c, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		b, e := runQuiet(c, l.tp("status", "--format", "json"))
		return e == nil && tpConnected(b)
	}
	for _, p := range l.children {
		if !p.Running() {
			return false
		}
	}
	return probeDNS(ctx, l.dnsAddress, "kubernetes.default.svc."+l.p.Cluster.Domain) == nil
}
func (l *Link) cleanup() {
	for i := len(l.children) - 1; i >= 0; i-- {
		l.children[i].Stop()
		<-l.children[i].done
	}
	l.children = nil
	if l.tpOwned {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		_, err := runQuiet(ctx, l.tp("quit"))
		cancel()
		l.mu.Lock()
		if err != nil {
			l.cleanupError = "Telepresence disconnect failed; inspect its connection and routes before reconnecting"
		} else {
			l.cleanupError = ""
			l.tpOwned = false
		}
		l.mu.Unlock()
	}
}
func freePort() (int, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return 0, e
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func ForwardCommand(p Profile, local int) []string {
	remote := local
	args := []string{"-n", p.Network.GatewayNamespace, "port-forward", "--address=127.0.0.1", "deployment/" + p.Network.GatewayDeployment, fmt.Sprintf("%d:%d", remote, p.Network.GatewayPort)}
	if p.Network.Transport == "kubectl" {
		return (Kube{p}).Command(args...)
	}
	base := append([]string{p.Cluster.RemoteKubectl, "--context", p.Cluster.Context}, args...)
	return []string{p.Network.SSH, "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=10", "-o", "ServerAliveCountMax=3", "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", local, remote), "--", p.Cluster.SSHHost, remoteCommand(base)}
}
func SingBoxConfig(p Profile, socks, dns int) map[string]any {
	suffix := append([]string{p.Cluster.Domain}, p.Network.DNSSuffixes...)
	routes := append(append([]string{}, p.Network.RouteCIDRs...), "198.18.0.2/32")
	return map[string]any{
		"log":       map[string]any{"level": "warn"},
		"dns":       map[string]any{"servers": []any{map[string]any{"type": "tcp", "tag": "cluster-dns", "server": "127.0.0.1", "server_port": dns}, map[string]any{"type": "udp", "tag": "corporate-dns", "server": p.Network.FallbackDNS}}, "rules": []any{map[string]any{"domain_suffix": suffix, "server": "cluster-dns"}}, "final": "corporate-dns", "strategy": "ipv4_only"},
		"inbounds":  []any{map[string]any{"type": "tun", "tag": "devctl-tun", "interface_name": "devctl", "address": []string{"198.18.0.1/30"}, "mtu": 1400, "auto_route": true, "strict_route": true, "route_address": routes, "route_exclude_address": p.Network.BypassCIDRs, "stack": "mixed"}},
		"outbounds": []any{map[string]any{"type": "socks", "tag": "cluster", "server": "127.0.0.1", "server_port": socks, "version": "5", "network": "tcp"}, map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"auto_detect_interface": true, "rules": []any{map[string]any{"action": "sniff"}, map[string]any{"protocol": "dns", "action": "hijack-dns"}, map[string]any{"ip_cidr": p.Network.RouteCIDRs, "network": "tcp", "outbound": "cluster"}, map[string]any{"ip_cidr": p.Network.RouteCIDRs, "action": "reject"}}, "final": "direct"},
	}
}
