package offline

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// PullOpts tunes the native (daemon-free) pull.
type PullOpts struct {
	// Creds is "user:password" for a private mirror. Empty falls back to the
	// docker keychain (~/.docker/config.json if it exists) and then anonymous —
	// which is what a Windows box with no Docker installed will use.
	Creds string
	// Insecure allows a plain-HTTP / self-signed internal registry.
	Insecure bool
}

// NativePull pulls srcRef for the given platform straight from the registry and
// writes it as a docker-archive tarball tagged canonicalRef. It needs no Docker
// (nor any container tooling) — ldbg speaks the registry protocol itself, so
// `ldbg bundle` works on a fresh Windows 11 laptop.
//
// Tagging the archive with canonicalRef (not srcRef) is what lets --from pull
// through any mirror while the bundle still carries the official
// ghcr.io/telepresenceio/tel2:<ver> name, so `ldbg cluster install` is unchanged.
// Proxies are honoured via HTTPS_PROXY/HTTP_PROXY (net/http's ProxyFromEnvironment).
func NativePull(ctx context.Context, srcRef, canonicalRef, platform, outPath string, o PullOpts) (int64, error) {
	p, err := v1.ParsePlatform(platform)
	if err != nil {
		return 0, fmt.Errorf("parse platform %q: %w", platform, err)
	}

	opts := []crane.Option{crane.WithContext(ctx), crane.WithPlatform(p)}
	if o.Creds != "" {
		user, pass, ok := strings.Cut(o.Creds, ":")
		if !ok {
			return 0, fmt.Errorf("--creds must be user:password")
		}
		opts = append(opts, crane.WithAuth(authn.FromConfig(authn.AuthConfig{Username: user, Password: pass})))
	} else {
		opts = append(opts, crane.WithAuthFromKeychain(authn.DefaultKeychain))
	}
	if o.Insecure {
		opts = append(opts, crane.Insecure)
	}

	img, err := crane.Pull(srcRef, opts...)
	if err != nil {
		return 0, fmt.Errorf("pull %s (%s): %w", srcRef, platform, err)
	}
	if err := crane.Save(img, canonicalRef, outPath); err != nil {
		// Layers stream as the archive is written, so a mid-transfer failure would
		// otherwise leave a truncated tar that looks like a usable bundle.
		os.Remove(outPath)
		return 0, fmt.Errorf("write %s: %w", outPath, err)
	}
	fi, err := os.Stat(outPath)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}
