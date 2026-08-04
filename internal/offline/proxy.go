package offline

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/http/httpproxy"
)

// Where a proxy setting came from, shown to the user so "my VPN is on but the
// pull times out" is answerable at a glance.
const (
	ProxySourceNone   = "no proxy"
	ProxySourceFlag   = "--proxy"
	ProxySourceEnv    = "environment"
	ProxySourceSystem = "Windows system proxy"
)

// Proxy is the resolved proxy configuration for the native (daemon-free) pull.
//
// This exists because Go's http.ProxyFromEnvironment reads ONLY HTTP(S)_PROXY —
// it never looks at the Windows system proxy that Clash/v2rayN-style VPN clients
// configure. Browsers work, ldbg silently goes direct and times out. Reading the
// registry closes that gap; printing Source makes it visible either way.
type Proxy struct {
	// Fn is the transport's proxy function; nil means connect directly.
	Fn func(*http.Request) (*url.URL, error)
	// Source is one of the ProxySource* constants.
	Source string
	// URL is the proxy in use ("" when direct), for display only.
	URL string
	// Note carries a caveat worth surfacing, e.g. an unsupported PAC script.
	Note string
}

// Describe renders the proxy for a progress line.
func (p Proxy) Describe() string {
	if p.URL == "" {
		return ProxySourceNone
	}
	return fmt.Sprintf("proxy %s (%s)", p.URL, p.Source)
}

// sysProxy is what an OS-level proxy configuration looks like. Only Windows
// populates it; elsewhere proxy configuration is already environment-based.
type sysProxy struct {
	HTTP, HTTPS, Bypass, PAC string
}

// ResolveProxy picks the proxy to use, in order: --proxy, then the environment
// (HTTPS_PROXY/HTTP_PROXY), then the OS-level setting, then direct. creds
// ("user:password") is applied to whichever of those wins — necessary because
// Windows keeps only the address in the registry, never the credentials, so an
// authenticated proxy cannot be discovered whole.
func ResolveProxy(explicit, creds string) (Proxy, error) {
	var httpP, httpsP, noProxy, source, note string

	switch sp, hasSys := systemProxy(); {
	case strings.TrimSpace(explicit) != "":
		u, err := parseProxyURL(explicit)
		if err != nil {
			return Proxy{}, err
		}
		httpP, httpsP = u.String(), u.String()
		noProxy = envNoProxy()
		source = ProxySourceFlag

	case envProxyName() != "":
		cfg := httpproxy.FromEnvironment()
		httpP, httpsP, noProxy = cfg.HTTPProxy, cfg.HTTPSProxy, cfg.NoProxy
		source = ProxySourceEnv + " (" + envProxyName() + ")"

	case hasSys && (sp.HTTP != "" || sp.HTTPS != ""):
		httpP, httpsP, noProxy = sp.HTTP, sp.HTTPS, windowsNoProxy(sp.Bypass)
		source = ProxySourceSystem
		if sp.PAC != "" {
			note = "a PAC script is also configured but cannot be evaluated; pass --proxy if this proxy is wrong"
		}

	default:
		p := Proxy{Source: ProxySourceNone}
		if hasSys && sp.PAC != "" {
			p.Note = "a PAC script (" + sp.PAC + ") is configured; ldbg cannot evaluate PAC — pass --proxy (add --proxy-creds if it needs a login)"
		}
		if strings.TrimSpace(creds) != "" {
			p.Note = "--proxy-creds was given but no proxy is configured; it will be ignored"
		}
		return p, nil
	}

	if creds = strings.TrimSpace(creds); creds != "" {
		var err error
		if httpP, err = withCreds(httpP, creds); err != nil {
			return Proxy{}, err
		}
		if httpsP, err = withCreds(httpsP, creds); err != nil {
			return Proxy{}, err
		}
	}

	cfg := httpproxy.Config{HTTPProxy: httpP, HTTPSProxy: httpsP, NoProxy: noProxy}
	fn := cfg.ProxyFunc()
	shown := httpsP
	if shown == "" {
		shown = httpP
	}
	return Proxy{
		Fn:     func(r *http.Request) (*url.URL, error) { return fn(r.URL) },
		Source: source,
		URL:    redactProxy(shown),
		Note:   note,
	}, nil
}

// withCreds attaches user:password to a proxy address. Credentials go through
// url.UserPassword rather than string concatenation so a password containing
// '@', '/' or ':' still works — embedding those in a URL by hand silently
// mis-parses (host becomes the fragment after the last '@').
func withCreds(raw, creds string) (string, error) {
	if raw == "" {
		return "", nil
	}
	user, pass, ok := strings.Cut(creds, ":")
	if !ok || user == "" {
		return "", fmt.Errorf("--proxy-creds must be user:password")
	}
	u, err := parseProxyURL(raw)
	if err != nil {
		return "", err
	}
	u.User = url.UserPassword(user, pass)
	return u.String(), nil
}

// redactProxy hides the password before the proxy is printed or written to JSON.
func redactProxy(raw string) string {
	u, err := parseProxyURL(raw)
	if err != nil || u.User == nil {
		return raw
	}
	return u.Redacted()
}

// envNoProxy is the NO_PROXY value in either spelling.
func envNoProxy() string {
	if v := os.Getenv("NO_PROXY"); v != "" {
		return v
	}
	return os.Getenv("no_proxy")
}

// envProxyName returns the name of the first proxy variable that is set.
func envProxyName() string {
	name, _ := envProxy()
	return name
}

// envProxy returns the first proxy variable that is set, and its value.
func envProxy() (name, value string) {
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return n, v
		}
	}
	return "", ""
}

// parseProxyURL accepts "host:port" as well as a full URL, defaulting to http://
// the way every other tool does.
func parseProxyURL(s string) (*url.URL, error) {
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid proxy %q: want http://host:port", s)
	}
	return u, nil
}

// parseWindowsProxyServer splits the registry's ProxyServer value, which is
// either one proxy for every protocol ("127.0.0.1:7890") or a per-protocol list
// ("http=127.0.0.1:7890;https=127.0.0.1:7890;ftp=...").
func parseWindowsProxyServer(v string) (httpProxy, httpsProxy string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	if !strings.Contains(v, "=") {
		return v, v
	}
	for _, part := range strings.Split(v, ";") {
		scheme, addr, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || addr == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(scheme)) {
		case "http":
			httpProxy = strings.TrimSpace(addr)
		case "https":
			httpsProxy = strings.TrimSpace(addr)
		}
	}
	// A per-protocol list that only names http still proxies https in practice.
	if httpsProxy == "" {
		httpsProxy = httpProxy
	}
	return httpProxy, httpsProxy
}

// windowsNoProxy converts the registry's ProxyOverride (semicolon separated, with
// the pseudo-entries <local> and <-loopback>) into the comma-separated form
// x/net/http/httpproxy understands. The pseudo-entries have no direct equivalent,
// so loopback is always bypassed — which is what <local> means in practice and
// keeps an internal registry on 127.0.0.1 reachable.
func windowsNoProxy(override string) string {
	out := []string{"localhost", "127.0.0.1", "::1"}
	for _, e := range strings.Split(override, ";") {
		e = strings.TrimSpace(e)
		if e == "" || strings.HasPrefix(e, "<") {
			continue
		}
		out = append(out, e)
	}
	return strings.Join(out, ",")
}
