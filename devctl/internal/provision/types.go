// Package provision implements offline administrator operations. It never shares
// the developer session daemon or automatically changes the mesh installation.
package provision

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type Artifact struct {
	ID             string `json:"id"`
	URL            string `json:"url,omitempty"`
	File           string `json:"file,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	ChecksumURL    string `json:"checksumURL,omitempty"`
	GitHubAssetAPI string `json:"githubAssetAPI,omitempty"`
	Member         string `json:"member,omitempty"`
	Target         string `json:"target"`
	Format         string `json:"format,omitempty"`
	HelmIndexURL   string `json:"helmIndexURL,omitempty"`
	ChartVersion   string `json:"chartVersion,omitempty"`
	ChartName      string `json:"chartName,omitempty"`
	HelmOCI        string `json:"helmOCI,omitempty"`
}
type Image struct {
	Name         string `json:"name"`
	Source       string `json:"source"`
	Platform     string `json:"platform"`
	File         string `json:"file"`
	Digest       string `json:"digest,omitempty"`
	SourceDigest string `json:"sourceDigest,omitempty"`
	Ref          string `json:"ref,omitempty"`
	ConfigDigest string `json:"configDigest,omitempty"`
}
type BundleConfig struct {
	Version      int        `json:"version"`
	Output       string     `json:"output"`
	Cache        string     `json:"cache"`
	Distribution string     `json:"distribution"`
	Artifacts    []Artifact `json:"artifacts"`
	Images       []Image    `json:"images"`
}
type LockedFile struct {
	Path           string `json:"path"`
	SHA256         string `json:"sha256"`
	Source         string `json:"source,omitempty"`
	UpstreamSHA256 string `json:"upstreamSHA256,omitempty"`
}
type Lock struct {
	Version int          `json:"version"`
	Files   []LockedFile `json:"files"`
	Images  []Image      `json:"images"`
}
type SSH struct {
	Host           string `json:"host"`
	Port           int    `json:"port,omitempty"`
	IdentityFile   string `json:"identityFile,omitempty"`
	KnownHostsFile string `json:"knownHostsFile,omitempty"`
	ProxyJump      string `json:"proxyJump,omitempty"`
	Sudo           bool   `json:"sudo,omitempty"`
}
type Node struct {
	Name    string `json:"name"`
	Arch    string `json:"arch"`
	SSH     SSH    `json:"ssh"`
	Runtime string `json:"runtime,omitempty"`
	Socket  string `json:"socket,omitempty"`
	Ctr     string `json:"ctr,omitempty"`
}
type Registry struct {
	Prefix     string `json:"prefix"`
	ConfigFile string `json:"configFile,omitempty"`
	PullSecret string `json:"pullSecret,omitempty"`
}
type Credentials struct {
	Mode   string   `json:"mode"`
	Secret string   `json:"secret"`
	Users  []string `json:"users,omitempty"`
}
type Dependency struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Service   string            `json:"service"`
	Port      int               `json:"port"`
	Selector  map[string]string `json:"selector,omitempty"`
}
type Install struct {
	Version            int              `json:"version"`
	Namespace          string           `json:"namespace"`
	Context            string           `json:"context"`
	Kubeconfig         string           `json:"kubeconfig"`
	Kubectl            string           `json:"kubectl,omitempty"`
	Bundle             string           `json:"bundle"`
	WorkDir            string           `json:"workDir"`
	RemoteDir          string           `json:"remoteDir"`
	SSH                SSH              `json:"ssh"`
	ImageMode          string           `json:"imageMode"`
	Registry           Registry         `json:"registry"`
	Nodes              []Node           `json:"nodes"`
	Backends           []string         `json:"backends"`
	ManagerRelease     string           `json:"managerRelease"`
	GatewayRelease     string           `json:"gatewayRelease"`
	ReuseManager       bool             `json:"reuseManager"`
	Credentials        Credentials      `json:"credentials"`
	DeveloperGroups    []string         `json:"developerGroups"`
	ConfigMaps         []string         `json:"configMaps"`
	Secrets            []string         `json:"secrets"`
	BusinessDeployment string           `json:"businessDeployment"`
	BusinessContainer  string           `json:"businessContainer"`
	DeveloperProfile   string           `json:"developerProfile"`
	Domain             string           `json:"domain"`
	ClusterDNS         string           `json:"clusterDNS"`
	RouteCIDRs         []string         `json:"routeCIDRs"`
	BypassCIDRs        []string         `json:"bypassCIDRs"`
	FallbackDNS        string           `json:"fallbackDNS"`
	Dependencies       []Dependency     `json:"dependencies"`
	ExtraEgress        []map[string]any `json:"extraEgress"`
	Tolerations        []map[string]any `json:"tolerations"`
	IstioNamespace     string           `json:"istioNamespace"`
	CNIDaemonSet       string           `json:"cniDaemonSet"`
	CNIConfigMap       string           `json:"cniConfigMap"`
	TimeoutSeconds     int              `json:"timeoutSeconds"`
	DeveloperSSHHost   string           `json:"developerSSHHost"`
	DeveloperContext   string           `json:"developerContext"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)
var shaRE = regexp.MustCompile(`^[a-f0-9]{64}$`)

func readJSON(path string, v any) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return fmt.Errorf("%s: %w", path, e)
	}
	if e = d.Decode(new(any)); e != io.EOF {
		return fmt.Errorf("%s: trailing JSON", path)
	}
	return nil
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(path, append(b, '\n'), 0600)
}
func relative(root, p string) (string, error) {
	if p == "" || strings.ContainsAny(p, "\\:\x00") || strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("unsafe bundle path %q", p)
	}
	for _, s := range strings.Split(p, "/") {
		if s == ".." || s == "." || s == "" {
			return "", fmt.Errorf("unsafe bundle path %q", p)
		}
	}
	return filepath.Join(root, filepath.FromSlash(p)), nil
}
func fileSHA(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	_, e = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), e
}
func resolve(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	joined := filepath.Join(base, p)
	absolute, e := filepath.Abs(joined)
	if e == nil {
		return absolute
	}
	return joined
}
func has(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func (s SSH) validate() error {
	if s.Host == "" || strings.HasPrefix(s.Host, "-") || strings.ContainsAny(s.Host, " \t\r\n'\";`$\\") {
		return fmt.Errorf("invalid SSH host")
	}
	if s.Port < 0 || s.Port > 65535 {
		return fmt.Errorf("invalid SSH port")
	}
	return nil
}
func loadInstall(path string) (Install, error) {
	var c Install
	if e := readJSON(path, &c); e != nil {
		return c, e
	}
	base := filepath.Dir(path)
	c.Bundle = resolve(base, c.Bundle)
	c.WorkDir = resolve(base, c.WorkDir)
	c.DeveloperProfile = resolve(base, c.DeveloperProfile)
	c.Registry.ConfigFile = resolve(base, c.Registry.ConfigFile)
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 300
	}
	if c.Kubectl == "" {
		c.Kubectl = tool(c.Bundle, "kubectl")
	}
	return c, nil
}
func (c Install) validate() error {
	if c.Version != 1 || c.Namespace != "kube-system" {
		return fmt.Errorf("version must be 1; this deployment profile requires existing kube-system")
	}
	for _, n := range []string{c.ManagerRelease, c.GatewayRelease, c.Credentials.Secret, c.BusinessDeployment, c.BusinessContainer, c.IstioNamespace, c.CNIDaemonSet, c.CNIConfigMap} {
		if !nameRE.MatchString(n) {
			return fmt.Errorf("invalid or missing Kubernetes name: %q", n)
		}
	}
	if c.Context == "" || c.Kubeconfig == "" || c.Bundle == "" || c.WorkDir == "" {
		return fmt.Errorf("context, kubeconfig, bundle and workDir are required")
	}
	if c.ImageMode != "registry" && c.ImageMode != "nodes" {
		return fmt.Errorf("imageMode must be registry or nodes")
	}
	if len(c.Nodes) == 0 {
		return fmt.Errorf("explicit nodes inventory is required")
	}
	seen := map[string]bool{}
	for _, n := range c.Nodes {
		if n.Arch != c.Nodes[0].Arch {
			return fmt.Errorf("one installation requires homogeneous eligible nodes; select one architecture")
		}
		if !nameRE.MatchString(n.Name) || seen[n.Name] || !has([]string{"amd64", "arm64"}, n.Arch) {
			return fmt.Errorf("invalid/duplicate node or architecture")
		}
		seen[n.Name] = true
		if c.ImageMode == "nodes" {
			if e := n.SSH.validate(); e != nil {
				return e
			}
		}
		if !has([]string{"", "auto", "containerd", "docker"}, n.Runtime) {
			return fmt.Errorf("unsupported runtime %q", n.Runtime)
		}
	}
	if len(c.Backends) == 0 {
		return fmt.Errorf("backends is required")
	}
	for _, b := range c.Backends {
		if !has([]string{"telepresence", "gateway"}, b) {
			return fmt.Errorf("invalid backend")
		}
	}
	if c.ImageMode == "registry" && (c.Registry.Prefix == "" || strings.Contains(c.Registry.Prefix, "://")) {
		return fmt.Errorf("registry.prefix must be registry host/path")
	}
	if !has([]string{"generate", "existingSecret"}, c.Credentials.Mode) {
		return fmt.Errorf("credentials.mode must be generate or existingSecret")
	}
	if c.Credentials.Mode == "generate" && len(c.Credentials.Users) == 0 {
		return fmt.Errorf("credentials.users is required")
	}
	for _, u := range c.Credentials.Users {
		if !nameRE.MatchString(u) {
			return fmt.Errorf("invalid credential user")
		}
	}
	if net.ParseIP(c.ClusterDNS) == nil || net.ParseIP(c.FallbackDNS) == nil || c.Domain == "" {
		return fmt.Errorf("explicit DNS addresses/domain required")
	}
	if len(c.RouteCIDRs) == 0 {
		return fmt.Errorf("routeCIDRs required")
	}
	for _, s := range append(append([]string{}, c.RouteCIDRs...), c.BypassCIDRs...) {
		ip, n, e := net.ParseCIDR(s)
		if e != nil || ip.To4() == nil {
			return fmt.Errorf("invalid IPv4 CIDR %q", s)
		}
		ones, _ := n.Mask.Size()
		if ones == 0 {
			return fmt.Errorf("default routes forbidden")
		}
	}
	if len(c.Dependencies) == 0 {
		return fmt.Errorf("at least one dependency is required for installation verification")
	}
	for _, d := range c.Dependencies {
		if !nameRE.MatchString(d.Namespace) || !nameRE.MatchString(d.Service) || d.Port < 1 || d.Port > 65535 {
			return fmt.Errorf("invalid dependency")
		}
	}
	if c.TimeoutSeconds < 10 || c.TimeoutSeconds > 3600 {
		return fmt.Errorf("timeoutSeconds must be 10..3600")
	}
	return nil
}
