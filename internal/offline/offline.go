// Package offline implements the air-gapped install path: the traffic-manager and
// injected traffic-agent both run ghcr.io/telepresenceio/tel2:<ver>, a single image.
// On an internet-connected machine `ldbg bundle` fetches it — natively (no Docker
// needed) or through a local docker — and writes a docker-archive tarball; inside
// the air-gapped environment `ldbg cluster install` imports it (internal registry or
// minikube/kind/k3d/ctr) and installs the traffic-manager from the telepresence
// client's embedded Helm chart with pullPolicy=IfNotPresent.
package offline

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// DefaultPlatform is what a bundle targets unless told otherwise. The bundle is
// for the *cluster*, not for the machine building it — defaulting to the host
// arch would silently produce an arm64 bundle on an Apple-silicon laptop and fail
// with "exec format error" inside an amd64 cluster.
const DefaultPlatform = "linux/amd64"

// defaultOutBase is the tarball name used for DefaultPlatform.
const defaultOutBase = "tel2-bundle.tar"

// ImageFor returns the OSS traffic-manager/agent image for a Telepresence version.
func ImageFor(version string) string {
	v := strings.TrimPrefix(version, "v")
	return "ghcr.io/telepresenceio/tel2:" + v
}

// ValidPlatform accepts os/arch[/variant]. A comma-separated list is rejected on
// purpose: one bundle carries one architecture, so every import path (docker load,
// minikube/kind/k3d, ctr) stays predictable.
func ValidPlatform(p string) error {
	if strings.Contains(p, ",") {
		return fmt.Errorf("one bundle carries one architecture: pass a single --platform and run bundle once per arch")
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return fmt.Errorf("platform %q must be os/arch[/variant], e.g. linux/amd64 or linux/arm64", p)
	}
	for _, s := range parts {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("platform %q must be os/arch[/variant], e.g. linux/amd64 or linux/arm64", p)
		}
	}
	return nil
}

// archOf returns the arch segment of a platform string ("linux/arm64/v8" → "arm64").
func archOf(platform string) string {
	parts := strings.Split(platform, "/")
	if len(parts) < 2 {
		return platform
	}
	return parts[1]
}

// DefaultOutPath names the tarball for a platform. Non-default platforms get the
// arch in the name so bundling amd64 and arm64 in the same directory can't
// silently overwrite one another.
func DefaultOutPath(platform string) string {
	if platform == "" || platform == DefaultPlatform {
		return defaultOutBase
	}
	return "tel2-bundle-" + archOf(platform) + ".tar"
}

// SourceImage resolves where to pull from. `from` is either a registry/path prefix
// (the image name is appended) or a complete image reference (used as-is), so both
//
//	--from harbor.corp/mirror
//	--from harbor.corp/x/tel2:2.29.0
//
// work. The bundle is always written under the canonical name regardless.
func SourceImage(canonical, from string) string {
	from = strings.TrimSuffix(strings.TrimSpace(from), "/")
	if from == "" {
		return canonical
	}
	// A tagged last segment means a complete reference — but only when there is a
	// path at all, so a bare "registry.local:5000" stays a registry prefix.
	if strings.Contains(from, "/") && strings.Contains(lastPathSegment(from), ":") {
		return from
	}
	return from + "/" + lastPathSegment(canonical)
}

// Caps records which --platform flags the local docker supports; older releases
// (pre-28) have neither, and the docker path degrades instead of failing.
type Caps struct {
	SavePlatform    bool
	InspectPlatform bool
}

// DetectCaps probes the docker CLI's help output (no side effects).
func DetectCaps(ctx context.Context) Caps {
	has := func(args ...string) bool {
		out, _ := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		return strings.Contains(string(out), "--platform")
	}
	return Caps{
		SavePlatform:    has("save", "--help"),
		InspectPlatform: has("image", "inspect", "--help"),
	}
}

// DockerAvailable reports whether a docker CLI *and* a reachable daemon exist.
func DockerAvailable(ctx context.Context) bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Run() == nil
}

// ImagePresent reports whether the image is already in the local docker image
// store for the requested platform — the common case when someone already ran
// `docker pull` by hand, and the reason `ldbg bundle` need not touch the network.
func ImagePresent(ctx context.Context, image, platform string, c Caps) bool {
	if c.InspectPlatform {
		// inspect --platform errors out when the local image is a different arch.
		return exec.CommandContext(ctx, "docker", "image", "inspect", "--platform", platform, image).Run() == nil
	}
	out, err := run(ctx, "docker", "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image)
	if err != nil {
		return false
	}
	got := strings.TrimSpace(out)
	// Accept linux/arm64 for a requested linux/arm64/v8 and vice versa.
	return got == platform || got == archPrefix(platform)
}

func archPrefix(platform string) string {
	parts := strings.Split(platform, "/")
	if len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return platform
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		return so.String(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "),
			firstNonEmpty(strings.TrimSpace(se.String()), err.Error()))
	}
	return so.String(), nil
}

// DockerPull fetches the image for a specific platform via the local docker.
func DockerPull(ctx context.Context, image, platform string) error {
	args := []string{"pull"}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	_, err := run(ctx, "docker", append(args, image)...)
	return err
}

// DockerTag aliases a mirror-pulled image under the canonical name so the saved
// archive carries the official reference.
func DockerTag(ctx context.Context, src, dst string) error {
	_, err := run(ctx, "docker", "tag", src, dst)
	return err
}

// DockerSave writes the image to a transfer tarball, pinning the platform when
// the local docker supports it (with the containerd image store one tag can hold
// several architectures).
func DockerSave(ctx context.Context, image, outPath, platform string, c Caps) error {
	args := []string{"save"}
	if c.SavePlatform && platform != "" {
		args = append(args, "--platform", platform)
	}
	_, err := run(ctx, "docker", append(args, image, "-o", outPath)...)
	return err
}

// FailKind classifies a fetch failure so the user gets an actionable next step
// instead of a raw registry/daemon error.
type FailKind string

const (
	FailDNS       FailKind = "dns"
	FailNet       FailKind = "net"
	FailProxyAuth FailKind = "proxy-auth"
	FailAuth      FailKind = "auth"
	FailNotFound  FailKind = "notfound"
	FailTLS       FailKind = "tls"
	FailDaemon    FailKind = "daemon"
	FailUnknown   FailKind = "unknown"
)

// Classify inspects a docker/registry error string.
//
// Order matters: docker wraps a failed token request in "failed to authorize", so
// a pure DNS outage surfaces as *both* an authorize failure and a lookup timeout.
// DNS is checked first — reporting that as an auth problem sends people hunting
// for credentials they never needed.
func Classify(s string) FailKind {
	l := strings.ToLower(s)
	dnsSymptom := strings.Contains(l, "i/o timeout") || strings.Contains(l, "no such host") ||
		strings.Contains(l, "server misbehaving") || strings.Contains(l, "temporary failure in name resolution")
	switch {
	case strings.Contains(l, "lookup ") && dnsSymptom:
		return FailDNS
	// Must precede FailAuth: a 407 also says "authentication required", but it is
	// the proxy asking, not the registry — pointing at --creds would be wrong.
	case strings.Contains(l, "proxy authentication required") || strings.Contains(l, "407 proxy") ||
		strings.Contains(l, "statuscode=407") || strings.Contains(l, "status code 407"):
		return FailProxyAuth
	case strings.Contains(l, "cannot connect to the docker daemon") ||
		strings.Contains(l, "is the docker daemon running") ||
		strings.Contains(l, "executable file not found") ||
		strings.Contains(l, "docker: command not found"):
		return FailDaemon
	case strings.Contains(l, "x509") || strings.Contains(l, "certificate") ||
		strings.Contains(l, "tls: "):
		return FailTLS
	case strings.Contains(l, "manifest unknown") || strings.Contains(l, "manifest_unknown") ||
		strings.Contains(l, "no child with platform") || strings.Contains(l, "no matching manifest") ||
		strings.Contains(l, "not found"):
		return FailNotFound
	case strings.Contains(l, "failed to authorize") || strings.Contains(l, "unauthorized") ||
		strings.Contains(l, "denied:") || strings.Contains(l, "authentication required") ||
		strings.Contains(l, "forbidden"):
		return FailAuth
	case isNetSymptom(l):
		return FailNet
	default:
		return FailUnknown
	}
}

// isNetSymptom matches connect-level failures. Windows phrases these completely
// differently from Unix — winsock returns "connectex: A connection attempt failed
// because the connected party did not properly respond…" (WSAETIMEDOUT) and "No
// connection could be made because the target machine actively refused it"
// (WSAECONNREFUSED) — so matching only the Unix strings silently drops every
// Windows network failure into "unknown".
func isNetSymptom(l string) bool {
	for _, s := range []string{
		// Unix
		"connection refused", "no route to host", "network is unreachable",
		"connection reset", "context deadline exceeded", "i/o timeout",
		// Windows (winsock)
		"connectex", "did not properly respond", "actively refused",
		"host has failed to respond", "a socket operation was attempted to an unreachable",
		"the semaphore timeout period has expired",
	} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// HintFor turns a FailKind into the concrete next step for this command. engine is
// the engine that just failed, so the advice never suggests the one already in use.
func HintFor(kind FailKind, image, platform, engine string, dockerPresent bool) string {
	switch kind {
	case FailDNS:
		h := "the registry hostname could not be resolved — check your network/VPN and DNS, then retry"
		if dockerPresent {
			h += "; if the image is already in your local docker, run 'ldbg bundle --no-pull'"
		}
		return h + "; behind a blocked/unstable ghcr.io, pull through a mirror: 'ldbg bundle --from <registry/path>'"
	case FailNet:
		return fmt.Sprintf("%s resolved but the connection timed out or was refused — this machine has no route to it (blocked network? VPN off?). %s, or pull through a reachable mirror: --from <registry/path>%s. You can also build the bundle on a machine that can reach it and copy the tar over — the archive is all the air-gapped side needs",
			registryOf(image), proxyHint(), altEngineHint(engine, dockerPresent))
	case FailProxyAuth:
		return "the PROXY rejected the request (407), not the registry — pass --proxy-creds user:password. Windows stores only the proxy address, never its credentials, so an authenticated proxy always needs them given explicitly (--creds is for the registry and will not help here)"
	case FailAuth:
		return "registry refused the credentials — for a private mirror pass --creds user:password (ghcr.io needs none for public images, so this usually means the anonymous token request itself failed: see DNS/proxy)"
	case FailNotFound:
		return fmt.Sprintf("no %s image for platform %s — check --tp-version and --platform (that tag may not publish this architecture)", image, platform)
	case FailTLS:
		return "TLS verification failed — for an internal registry with a self-signed cert pass --insecure, or install its CA in the system trust store"
	case FailDaemon:
		return "docker is not usable here — the default engine needs no Docker at all: retry with '--engine native' (or start Docker if you want the docker path)"
	default:
		return fmt.Sprintf("pull through a mirror with --from <registry/path>%s, or build the bundle where the registry is reachable and copy the tar over", altEngineHint(engine, dockerPresent))
	}
}

// proxyHint spells the proxy variable the way the user's shell wants it.
func proxyHint() string {
	if runtime.GOOS == "windows" {
		return `set a proxy ($env:HTTPS_PROXY = "http://<host>:<port>")`
	}
	return `set a proxy (export HTTPS_PROXY=http://<host>:<port>)`
}

// altEngineHint suggests the *other* engine, never the one that just failed.
func altEngineHint(engine string, dockerPresent bool) string {
	switch {
	case engine == "native" && dockerPresent:
		return ", try --engine docker (its proxy/registry-mirror settings may differ)"
	case engine == "docker":
		return ", try --engine native (no Docker, honours HTTPS_PROXY directly)"
	default:
		return ""
	}
}

// registryOf names the registry host in an image reference, for readable hints.
func registryOf(image string) string {
	if i := strings.Index(image, "/"); i > 0 {
		return image[:i]
	}
	if image == "" {
		return "the registry"
	}
	return image
}

// Importer describes how to load the bundled image into the air-gapped cluster.
type Importer string

const (
	ImportMinikube Importer = "minikube"
	ImportKind     Importer = "kind"
	ImportK3d      Importer = "k3d"
	ImportCtr      Importer = "ctr"      // containerd on each node (manual scp first)
	ImportRegistry Importer = "registry" // push to an internal registry
)

// ImportBundle loads the tarball/image into the cluster per the chosen method.
// For registry, image is re-tagged under registryPath and pushed; the caller then
// installs with image.registry=registryPath.
func ImportBundle(ctx context.Context, method Importer, tarPath, image, registryPath string) (string, error) {
	switch method {
	case ImportMinikube:
		return run(ctx, "minikube", "image", "load", tarPath)
	case ImportKind:
		return run(ctx, "kind", "load", "image-archive", tarPath)
	case ImportK3d:
		// k3d import works on an image tar.
		return run(ctx, "k3d", "image", "import", tarPath)
	case ImportRegistry:
		if registryPath == "" {
			return "", fmt.Errorf("registry import needs --registry <host/path>")
		}
		dst := registryPath + "/" + lastPathSegment(image)
		if _, err := run(ctx, "docker", "load", "-i", tarPath); err != nil {
			return "", err
		}
		if _, err := run(ctx, "docker", "tag", image, dst); err != nil {
			return "", err
		}
		return run(ctx, "docker", "push", dst)
	case ImportCtr:
		return "", fmt.Errorf("ctr import is per-node: scp %s to each node and run 'ctr -n k8s.io images import %s'", tarPath, tarPath)
	default:
		return "", fmt.Errorf("unknown import method %q", method)
	}
}

func lastPathSegment(image string) string {
	if i := strings.LastIndex(image, "/"); i >= 0 {
		return image[i+1:]
	}
	return image
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
