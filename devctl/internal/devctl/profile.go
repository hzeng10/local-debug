package devctl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Profiles are trusted local launch configuration, never downloaded executable code.
type Profile struct {
	Version      int          `json:"version"`
	Name         string       `json:"name"`
	Cluster      Cluster      `json:"cluster"`
	Network      Network      `json:"network"`
	Source       Source       `json:"source"`
	Application  Application  `json:"application"`
	Dependencies []Dependency `json:"dependencies,omitempty"`
	Path         string       `json:"-"`
}
type Cluster struct {
	Context       string `json:"context"`
	Namespace     string `json:"namespace"`
	Domain        string `json:"domain"`
	Kubectl       string `json:"kubectl,omitempty"`
	SSHHost       string `json:"sshHost,omitempty"`
	RemoteKubectl string `json:"remoteKubectl,omitempty"`
}
type Network struct {
	Backend           string   `json:"backend"`
	Transport         string   `json:"transport"`
	ManagerNamespace  string   `json:"managerNamespace,omitempty"`
	GatewayNamespace  string   `json:"gatewayNamespace"`
	GatewayDeployment string   `json:"gatewayDeployment"`
	GatewayPort       int      `json:"gatewayPort,omitempty"`
	Telepresence      string   `json:"telepresence,omitempty"`
	Chisel            string   `json:"chisel,omitempty"`
	SingBox           string   `json:"singBox,omitempty"`
	SSH               string   `json:"ssh,omitempty"`
	AuthEnv           string   `json:"authEnv,omitempty"`
	Fingerprint       string   `json:"fingerprint,omitempty"`
	RouteCIDRs        []string `json:"routeCIDRs,omitempty"`
	BypassCIDRs       []string `json:"bypassCIDRs,omitempty"`
	ClusterDNS        string   `json:"clusterDNS,omitempty"`
	FallbackDNS       string   `json:"fallbackDNS,omitempty"`
	DNSSuffixes       []string `json:"dnsSuffixes,omitempty"`
}
type Source struct {
	Deployment  string            `json:"deployment"`
	Container   string            `json:"container"`
	EnvAllow    []string          `json:"envAllow"`
	RequiredEnv []string          `json:"requiredEnv,omitempty"`
	Overrides   map[string]string `json:"overrides,omitempty"`
	Files       []ConfigFile      `json:"files,omitempty"`
}
type ConfigFile struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Key  string `json:"key"`
	Path string `json:"path"`
}
type Application struct {
	WorkDir                 string              `json:"workDir"`
	Command                 []string            `json:"command"`
	Java                    string              `json:"java,omitempty"`
	HealthURL               string              `json:"healthURL"`
	HealthStatus            int                 `json:"healthStatus,omitempty"`
	StartupSeconds          int                 `json:"startupSeconds,omitempty"`
	BackgroundTasksReviewed bool                `json:"backgroundTasksReviewed"`
	Tests                   map[string][]string `json:"tests,omitempty"`
	TestTimeoutSeconds      int                 `json:"testTimeoutSeconds,omitempty"`
	InheritEnv              []string            `json:"inheritEnv,omitempty"`
}
type Dependency struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	URL     string `json:"url,omitempty"`
	Status  int    `json:"status,omitempty"`
}

var identifier = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var sshHost = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.@-]*$`)

func LoadProfile(path, transport, host string) (Profile, error) {
	var p Profile
	abs, err := filepath.Abs(path)
	if err != nil {
		return p, err
	}
	f, err := os.Open(abs)
	if err != nil {
		return p, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1024*1024))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid profile JSON: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return p, fmt.Errorf("profile must contain exactly one JSON object")
	}
	p.Path = abs
	if transport != "" {
		p.Network.Transport = transport
	}
	if host != "" {
		p.Cluster.SSHHost = host
	}
	if p.Network.Transport == "ssh" {
		p.Network.Backend = "gateway"
	}
	if p.Cluster.Kubectl == "" {
		p.Cluster.Kubectl = "kubectl"
	}
	if p.Cluster.RemoteKubectl == "" {
		p.Cluster.RemoteKubectl = "kubectl"
	}
	if p.Network.Telepresence == "" {
		p.Network.Telepresence = "telepresence"
	}
	if p.Network.Chisel == "" {
		p.Network.Chisel = "chisel"
	}
	if p.Network.SingBox == "" {
		p.Network.SingBox = "sing-box"
	}
	if p.Network.SSH == "" {
		p.Network.SSH = "ssh"
	}
	if p.Network.GatewayPort == 0 {
		p.Network.GatewayPort = 8080
	}
	if p.Application.Java == "" {
		p.Application.Java = "java"
	}
	if p.Application.StartupSeconds == 0 {
		p.Application.StartupSeconds = 180
	}
	if p.Application.HealthStatus == 0 {
		p.Application.HealthStatus = 200
	}
	if p.Application.TestTimeoutSeconds == 0 {
		p.Application.TestTimeoutSeconds = 300
	}
	if !filepath.IsAbs(p.Application.WorkDir) {
		p.Application.WorkDir = filepath.Join(filepath.Dir(abs), p.Application.WorkDir)
	}
	return p, p.Validate()
}

func (p Profile) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("profile version must be 1")
	}
	for key, value := range map[string]string{"name": p.Name, "namespace": p.Cluster.Namespace, "domain": p.Cluster.Domain, "source.deployment": p.Source.Deployment, "source.container": p.Source.Container, "gatewayNamespace": p.Network.GatewayNamespace, "gatewayDeployment": p.Network.GatewayDeployment} {
		if len(value) > 253 || !identifier.MatchString(value) {
			return fmt.Errorf("invalid %s", key)
		}
	}
	if p.Cluster.Context == "" {
		return fmt.Errorf("cluster.context must be explicit")
	}
	if p.Network.Transport != "kubectl" && p.Network.Transport != "ssh" {
		return fmt.Errorf("transport must be kubectl or ssh")
	}
	if p.Network.Transport == "ssh" && !sshHost.MatchString(p.Cluster.SSHHost) {
		return fmt.Errorf("sshHost must be an SSH config alias or user@host; configure ports/keys in SSH config")
	}
	if p.Network.Backend != "telepresence" && p.Network.Backend != "gateway" {
		return fmt.Errorf("backend must be telepresence or gateway")
	}
	if p.Network.Backend == "telepresence" && (p.Network.Transport != "kubectl" || !identifier.MatchString(p.Network.ManagerNamespace)) {
		return fmt.Errorf("telepresence requires kubectl transport and managerNamespace")
	}
	if p.Network.Backend == "gateway" {
		if len(p.Network.Fingerprint) != 44 {
			return fmt.Errorf("gateway fingerprint must be the administrator-provided 44-character SHA256 fingerprint")
		}
		if !envName.MatchString(p.Network.AuthEnv) || p.Network.Fingerprint == "" {
			return fmt.Errorf("gateway requires authEnv and a pinned fingerprint")
		}
		if len(p.Network.RouteCIDRs) == 0 {
			return fmt.Errorf("gateway requires explicit routeCIDRs")
		}
		for _, ip := range []string{p.Network.ClusterDNS, p.Network.FallbackDNS} {
			if net.ParseIP(ip) == nil {
				return fmt.Errorf("clusterDNS and fallbackDNS must be literal IP addresses")
			}
		}
		if p.Network.GatewayPort < 1 || p.Network.GatewayPort > 65535 {
			return fmt.Errorf("invalid gatewayPort")
		}
	}
	for _, cidr := range append(append([]string{}, p.Network.RouteCIDRs...), p.Network.BypassCIDRs...) {
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() || prefix.Bits() == 0 {
			return fmt.Errorf("invalid IPv4 route %q (default routes are forbidden)", cidr)
		}
	}
	for _, suffix := range p.Network.DNSSuffixes {
		if !identifier.MatchString(suffix) {
			return fmt.Errorf("invalid DNS suffix")
		}
	}
	for _, key := range append(append([]string{}, p.Source.EnvAllow...), p.Source.RequiredEnv...) {
		if !envName.MatchString(key) {
			return fmt.Errorf("invalid environment name %q", key)
		}
	}
	for key := range p.Source.Overrides {
		if !envName.MatchString(key) {
			return fmt.Errorf("invalid override key")
		}
	}
	if len(p.Source.EnvAllow) == 0 {
		return fmt.Errorf("envAllow must explicitly list the variables to import")
	}
	for _, f := range p.Source.Files {
		if (f.Kind != "configmap" && f.Kind != "secret") || !identifier.MatchString(f.Name) || f.Key == "" || !safeRelative(f.Path) {
			return fmt.Errorf("invalid configuration file mapping")
		}
	}
	if len(p.Application.Command) == 0 || p.Application.StartupSeconds < 1 || p.Application.TestTimeoutSeconds < 1 {
		return fmt.Errorf("application command and positive timeouts are required")
	}
	u, err := url.Parse(p.Application.HealthURL)
	if err != nil || u.Scheme != "http" || u.User != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		return fmt.Errorf("healthURL must be a loopback HTTP URL")
	}
	if !p.Application.BackgroundTasksReviewed {
		return fmt.Errorf("backgroundTasksReviewed must be true after reviewing consumers, schedulers and migrations")
	}
	for _, dep := range p.Dependencies {
		if dep.Name == "" || (dep.Address == "") == (dep.URL == "") {
			return fmt.Errorf("dependency requires name and exactly one address or URL")
		}
		if dep.Address != "" {
			if _, _, err := net.SplitHostPort(dep.Address); err != nil {
				return fmt.Errorf("invalid dependency address")
			}
		}
		if dep.URL != "" {
			u, e := url.Parse(dep.URL)
			if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("invalid dependency URL")
			}
		}
	}
	return nil
}
func safeRelative(s string) bool {
	if s == "" || strings.ContainsAny(s, `\:`) || filepath.IsAbs(s) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." || part == "." || part == "" {
			return false
		}
	}
	return true
}
func (p Profile) NetworkKey() string {
	// Application namespaces/configuration do not change the one shared network session.
	b, _ := json.Marshal(struct {
		Cluster Cluster
		Network Network
	}{Cluster: Cluster{Context: p.Cluster.Context, Domain: p.Cluster.Domain, Kubectl: p.Cluster.Kubectl, SSHHost: p.Cluster.SSHHost, RemoteKubectl: p.Cluster.RemoteKubectl}, Network: p.Network})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (p Profile) Digest() string {
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
