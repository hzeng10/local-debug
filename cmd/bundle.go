package cmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/hzeng10/local-debug/internal/offline"
	"github.com/spf13/cobra"
)

var (
	bundleOut       string
	bundleVersion   string
	bundleNoPull    bool
	bundlePlatform  string
	bundleFrom      string
	bundleEngine    string
	bundleCreds     string
	bundleInsecure  bool
	bundleForcePull bool
	bundleProxy     string
	bundleProxyCred string
)

// bundleResult is the --json payload for `ldbg bundle`.
type bundleResult struct {
	Image       string `json:"image"`
	Tarball     string `json:"tarball"`
	Platform    string `json:"platform"`
	Engine      string `json:"engine"`
	Source      string `json:"source,omitempty"`
	Pulled      bool   `json:"pulled"`
	SkippedPull bool   `json:"skippedPull"`
	SizeBytes   int64  `json:"sizeBytes"`
	Proxy       string `json:"proxy,omitempty"`
	ProxySource string `json:"proxySource,omitempty"`
}

var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "On an internet machine, save the traffic-manager image as a transfer bundle",
	Long: `bundle resolves the traffic-manager/agent image for the targeted Telepresence
version (ghcr.io/telepresenceio/tel2:<ver> — one image serves both manager and agent)
and writes it as a docker-archive tarball you carry into the air-gapped environment,
where 'ldbg cluster install' imports it and installs the traffic-manager.

Engines (--engine):
  auto   (default) use the local docker when it already holds the image for the
         requested platform (no network at all), otherwise pull natively
  native pull straight from the registry — needs NO Docker or container tooling,
         so this works on a fresh Windows 11 laptop
  docker docker pull + docker save

Proxies (native engine): --proxy wins, else HTTPS_PROXY/HTTP_PROXY, else the
Windows system proxy (what a proxy-mode VPN client configures — Go does not read
it on its own). The proxy actually in effect is printed with every pull, with the
password redacted. Windows records only the proxy address, never its credentials,
so an authenticated proxy needs --proxy-creds user:password (safer than embedding
them in a URL, where a password containing '@' or '/' mis-parses).

The bundle targets the CLUSTER's architecture, not this machine's: --platform
defaults to linux/amd64. For an arm64 cluster run with --platform linux/arm64
(check with 'ldbg cluster preflight'). One bundle carries one architecture.

Where ghcr.io is blocked or unstable, pull through a mirror with
--from <registry/path>; the archive is still written under the official image
name, so the air-gapped install steps do not change.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()

		if err := offline.ValidPlatform(bundlePlatform); err != nil {
			return out.Failf("bundle", "e.g. --platform linux/amd64 or --platform linux/arm64", err)
		}
		canonical := offline.ImageFor(bundleVersion)
		src := offline.SourceImage(canonical, bundleFrom)
		outPath := bundleOut
		if !cmd.Flags().Changed("out") {
			outPath = offline.DefaultOutPath(bundlePlatform)
		}

		dockerOK := offline.DockerAvailable(ctx)
		var caps offline.Caps
		if dockerOK {
			caps = offline.DetectCaps(ctx)
		}
		engine, err := resolveBundleEngine(ctx, canonical, dockerOK, caps)
		if err != nil {
			return out.Failf("bundle", "valid engines: auto|native|docker — 'native' needs no Docker at all", err)
		}

		// Source is reported only when a fetch actually goes through the mirror —
		// a skipped pull uses the local image, whatever --from says.
		res := bundleResult{
			Image: canonical, Tarball: outPath, Platform: bundlePlatform, Engine: engine,
		}

		if engine == "native" {
			// Resolve the proxy explicitly and say so: on Windows a proxy-mode VPN
			// configures the system proxy, which Go's default transport ignores —
			// the pull would silently go direct and time out with the VPN "on".
			proxy, perr := offline.ResolveProxy(bundleProxy, bundleProxyCred)
			if perr != nil {
				return out.Failf("bundle", "--proxy wants http://host:port; --proxy-creds wants user:password", perr)
			}
			res.Proxy, res.ProxySource = proxy.URL, proxy.Source
			if proxy.Note != "" {
				out.Info("! %s", proxy.Note)
			}
			out.Info("… pulling %s (%s) straight from the registry — no Docker needed, %s",
				src, bundlePlatform, proxy.Describe())
			size, perr := offline.NativePull(ctx, src, canonical, bundlePlatform, outPath,
				offline.PullOpts{Creds: bundleCreds, Insecure: bundleInsecure, Proxy: proxy})
			if perr != nil {
				return bundleFail(perr, src, engine, dockerOK, proxy)
			}
			res.Pulled, res.SizeBytes = true, size
			if src != canonical {
				res.Source = src
			}
			out.Result("bundle", bundleHuman(res), res)
			return nil
		}

		// docker engine
		switch {
		case bundleNoPull:
			// Shipping the wrong architecture into an air-gapped cluster fails late
			// and confusingly ("exec format error"), so refuse rather than guess.
			if !offline.ImagePresent(ctx, canonical, bundlePlatform, caps) {
				return out.Failf("bundle",
					"drop --no-pull to fetch it, or pass the --platform matching the image you pulled",
					fmt.Errorf("the local docker store has no %s for platform %s", canonical, bundlePlatform))
			}
			res.SkippedPull = true
			out.Info("… --no-pull: using the image already in the local docker store")
		case !bundleForcePull && offline.ImagePresent(ctx, canonical, bundlePlatform, caps):
			res.SkippedPull = true
			out.Info("✓ %s (%s) is already in the local docker store — skipping pull (--force-pull to re-fetch)",
				canonical, bundlePlatform)
		default:
			out.Info("… docker pull --platform %s %s", bundlePlatform, src)
			if perr := offline.DockerPull(ctx, src, bundlePlatform); perr != nil {
				return bundleFail(perr, src, engine, dockerOK, offline.Proxy{})
			}
			if src != canonical {
				if terr := offline.DockerTag(ctx, src, canonical); terr != nil {
					return out.Failf("bundle", "could not re-tag the mirrored image under its official name", terr)
				}
				res.Source = src
			}
			res.Pulled = true
		}

		out.Info("… docker save → %s", outPath)
		if serr := offline.DockerSave(ctx, canonical, outPath, bundlePlatform, caps); serr != nil {
			return bundleFail(serr, src, engine, dockerOK, offline.Proxy{})
		}
		if fi, ferr := os.Stat(outPath); ferr == nil {
			res.SizeBytes = fi.Size()
		}
		out.Result("bundle", bundleHuman(res), res)
		return nil
	},
}

// resolveBundleEngine picks the fetch engine. "auto" prefers the local docker only
// when it can satisfy the request without the network (the image is already there),
// and otherwise falls back to the daemon-free native pull.
func resolveBundleEngine(ctx context.Context, image string, dockerOK bool, caps offline.Caps) (string, error) {
	switch bundleEngine {
	case "native":
		if bundleNoPull {
			return "", fmt.Errorf("--no-pull reads the local docker image store, so it cannot be combined with --engine native")
		}
		return "native", nil
	case "docker":
		if !dockerOK {
			return "", fmt.Errorf("--engine docker was requested but no usable docker was found")
		}
		return "docker", nil
	case "auto":
		if bundleNoPull {
			if !dockerOK {
				return "", fmt.Errorf("--no-pull reads the local docker image store, but no usable docker was found")
			}
			return "docker", nil
		}
		if dockerOK && offline.ImagePresent(ctx, image, bundlePlatform, caps) {
			return "docker", nil
		}
		return "native", nil
	default:
		return "", fmt.Errorf("unknown --engine %q (want auto, native or docker)", bundleEngine)
	}
}

// bundleFail classifies the failure and adds the one next step that actually
// applies — plus, for DNS failures, whether the outage is this machine's or only
// the docker daemon's.
// It takes the *source* reference, not the canonical one: with --from it is the
// mirror that was unreachable, and naming ghcr.io there would be a lie.
func bundleFail(err error, src, engine string, dockerOK bool, proxy offline.Proxy) error {
	kind := offline.Classify(err.Error())
	hint := offline.HintFor(kind, src, bundlePlatform, engine, dockerOK)
	// "my VPN is on but it times out" is usually ldbg going direct: say what was
	// actually in effect before offering advice. Skipped only for a missing
	// image/platform, where the proxy is irrelevant.
	if engine == "native" && kind != offline.FailNotFound {
		hint = "the pull ran with " + proxy.Describe() + "; " + hint
	}
	if kind == offline.FailDNS {
		host := registryHost(src)
		if diag, selfCheck := dnsDiagnosis(host); diag != "" {
			hint = diag + "; " + hint
			if selfCheck != "" {
				out.Info("%s", selfCheck)
			}
		}
	}
	return out.Failf("bundle", hint, err)
}

// dnsDiagnosis resolves the registry host from ldbg itself, which separates "this
// machine has no working DNS" from "only the docker daemon can't resolve".
func dnsDiagnosis(host string) (diagnosis, selfCheck string) {
	if host == "" {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil || len(addrs) == 0 {
		d := fmt.Sprintf("ldbg's own lookup of %q failed too — the resolver/VPN on this machine is the problem, not the registry", host)
		if runtime.GOOS == "linux" {
			return d, fmt.Sprintf("self-check: resolvectl status | getent hosts %s | ip route get 1.1.1.1", host)
		}
		return d, ""
	}
	d := fmt.Sprintf("ldbg resolved %s → %s, so the failure is the docker daemon's own DNS/proxy", host, addrs[0])
	if runtime.GOOS == "linux" {
		return d, "self-check: sudo systemctl restart docker (or use the daemon-free path: --engine native)"
	}
	return d, "self-check: restart Docker Desktop, or use the daemon-free path: --engine native"
}

// registryHost extracts the registry hostname from an image reference.
func registryHost(ref string) string {
	if i := strings.Index(ref, "/"); i > 0 {
		return ref[:i]
	}
	return ""
}

func bundleHuman(r bundleResult) string {
	s := fmt.Sprintf("Bundled %s (%s, via %s engine) → %s", r.Image, r.Platform, r.Engine, r.Tarball)
	if r.SizeBytes > 0 {
		s += fmt.Sprintf(" (%.1f MB)", float64(r.SizeBytes)/(1024*1024))
	}
	if r.Source != "" {
		s += "\n  pulled from mirror: " + r.Source + " (archive carries the official name)"
	}
	return s + "\nCarry it to the air-gapped env, then: ldbg cluster install --bundle " + r.Tarball
}

func init() {
	f := bundleCmd.Flags()
	f.StringVar(&bundleOut, "out", "tel2-bundle.tar", "output tarball path (default gains the arch for non-default --platform)")
	f.StringVar(&bundleVersion, "tp-version", TelepresenceVersion, "Telepresence version to bundle")
	f.StringVar(&bundlePlatform, "platform", offline.DefaultPlatform, "target cluster platform: os/arch[/variant] (one bundle = one arch)")
	f.StringVar(&bundleFrom, "from", "", "pull through a mirror: registry/path prefix, or a complete image reference")
	f.StringVar(&bundleEngine, "engine", "auto", "how to fetch the image: auto|native|docker (native needs no Docker)")
	f.StringVar(&bundleCreds, "creds", "", "registry credentials as user:password (for a private mirror)")
	f.BoolVar(&bundleInsecure, "insecure", false, "allow a plain-HTTP / self-signed registry (native engine)")
	f.StringVar(&bundleProxy, "proxy", "", "proxy for the native engine, e.g. http://127.0.0.1:7890 (default: HTTPS_PROXY/HTTP_PROXY, then the Windows system proxy)")
	f.StringVar(&bundleProxyCred, "proxy-creds", "", "credentials for that proxy as user:password (Windows never stores these, so an authenticated proxy needs them here)")
	f.BoolVar(&bundleForcePull, "force-pull", false, "re-fetch even when the image is already in the local docker store")
	f.BoolVar(&bundleNoPull, "no-pull", false, "skip the fetch and save the image already in the local docker store")
	rootCmd.AddCommand(bundleCmd)
}
