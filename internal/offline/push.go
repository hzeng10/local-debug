package offline

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// PushOpts mirrors PullOpts: credentials for the destination registry, TLS
// relaxation for an internal one, and the resolved proxy.
type PushOpts struct {
	Creds    string // "user:password" for the internal registry
	Insecure bool   // plain-HTTP / self-signed internal registry
	Proxy    Proxy
}

// NativePush reads the bundle written by `ldbg bundle` and pushes it to an
// internal registry — without Docker, matching the bundle side. Every node then
// pulls from that registry, which is the only import path that scales past a
// handful of nodes.
//
// It returns the full destination reference so the caller can point the Helm
// values at it.
func NativePush(ctx context.Context, tarPath, dst string, o PushOpts) (string, error) {
	img, err := crane.Load(tarPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", tarPath, err)
	}

	tr := remote.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = o.Proxy.Fn
	if o.Insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // opt-in via --insecure
	}
	opts := []crane.Option{crane.WithContext(ctx), crane.WithTransport(tr)}
	if o.Creds != "" {
		user, pass, ok := strings.Cut(o.Creds, ":")
		if !ok {
			return "", fmt.Errorf("--creds must be user:password")
		}
		opts = append(opts, crane.WithAuth(authn.FromConfig(authn.AuthConfig{Username: user, Password: pass})))
	} else {
		opts = append(opts, crane.WithAuthFromKeychain(authn.DefaultKeychain))
	}
	if o.Insecure {
		opts = append(opts, crane.Insecure)
	}

	if err := crane.Push(img, dst, opts...); err != nil {
		return "", fmt.Errorf("push %s: %w", dst, err)
	}
	return dst, nil
}

// PushDestination is where the bundled image lands in the internal registry:
// the registry path plus the image's own name:tag, so the cluster can be pointed
// at it with images.registry alone.
func PushDestination(registryPath, canonicalImage string) string {
	return strings.TrimRight(registryPath, "/") + "/" + lastPathSegment(canonicalImage)
}
