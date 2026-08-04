package offline

import (
	"net/http"
	"strings"
	"testing"
)

// Registry values seen from the proxy-mode VPN clients developers actually use
// (Clash on 7890, v2rayN on 10809), plus the per-protocol form Windows writes
// when each scheme is configured separately.
func TestParseWindowsProxyServer(t *testing.T) {
	cases := []struct {
		in                  string
		wantHTTP, wantHTTPS string
	}{
		{"", "", ""},
		{"127.0.0.1:7890", "127.0.0.1:7890", "127.0.0.1:7890"},
		{"http://127.0.0.1:10809", "http://127.0.0.1:10809", "http://127.0.0.1:10809"},
		{"http=127.0.0.1:7890;https=127.0.0.1:7890", "127.0.0.1:7890", "127.0.0.1:7890"},
		{"http=127.0.0.1:8080;ftp=10.0.0.1:21", "127.0.0.1:8080", "127.0.0.1:8080"},
		{"https=proxy.corp:3128", "", "proxy.corp:3128"},
		{" http = 127.0.0.1:7890 ; https = 127.0.0.1:7891 ", "127.0.0.1:7890", "127.0.0.1:7891"},
	}
	for _, c := range cases {
		gotHTTP, gotHTTPS := parseWindowsProxyServer(c.in)
		if gotHTTP != c.wantHTTP || gotHTTPS != c.wantHTTPS {
			t.Errorf("parseWindowsProxyServer(%q) = (%q, %q), want (%q, %q)",
				c.in, gotHTTP, gotHTTPS, c.wantHTTP, c.wantHTTPS)
		}
	}
}

func TestWindowsNoProxyKeepsLoopbackReachable(t *testing.T) {
	got := windowsNoProxy("*.corp.local;10.*;<local>;<-loopback>")
	for _, want := range []string{"localhost", "127.0.0.1", "*.corp.local", "10.*"} {
		if !strings.Contains(got, want) {
			t.Errorf("no-proxy %q must contain %q", got, want)
		}
	}
	// The pseudo-entries have no equivalent and must not leak through as hosts.
	if strings.Contains(got, "<") {
		t.Errorf("no-proxy %q leaked a pseudo-entry", got)
	}
	// An internal registry on localhost must never be sent to the proxy.
	if got := windowsNoProxy(""); !strings.Contains(got, "127.0.0.1") {
		t.Errorf("loopback must be bypassed even with no override: %q", got)
	}
}

func TestParseProxyURL(t *testing.T) {
	for _, in := range []string{"127.0.0.1:7890", "http://127.0.0.1:7890", "https://proxy.corp:3128"} {
		u, err := parseProxyURL(in)
		if err != nil {
			t.Fatalf("parseProxyURL(%q) = %v", in, err)
		}
		if u.Scheme == "" || u.Host == "" {
			t.Errorf("parseProxyURL(%q) = %q, want scheme+host", in, u)
		}
	}
	for _, bad := range []string{"", "   ", "://nope"} {
		if _, err := parseProxyURL(bad); err == nil {
			t.Errorf("parseProxyURL(%q) = nil error, want failure", bad)
		}
	}
}

// Windows keeps only the proxy address in the registry — never the credentials —
// so an authenticated proxy must be completable from the command line, and the
// password must never reach stdout or --json.
func TestProxyCredentials(t *testing.T) {
	clearProxyEnv(t)

	p, err := ResolveProxy("http://proxy.corp:3128", "alice:s3cr3t")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", "https://ghcr.io/v2/", nil)
	u, err := p.Fn(req)
	if err != nil || u == nil {
		t.Fatalf("proxy func = %v, %v", u, err)
	}
	pass, _ := u.User.Password()
	if u.User.Username() != "alice" || pass != "s3cr3t" {
		t.Errorf("credentials not attached to the proxy: %v", u.User)
	}
	// Displayed and JSON-serialised forms must not carry the password.
	if strings.Contains(p.URL, "s3cr3t") || strings.Contains(p.Describe(), "s3cr3t") {
		t.Errorf("password leaked into output: URL=%q describe=%q", p.URL, p.Describe())
	}
	if !strings.Contains(p.URL, "alice") {
		t.Errorf("username should stay visible for diagnosis: %q", p.URL)
	}

	// A password full of URL metacharacters must survive — this is exactly what
	// breaks when credentials are pasted into the URL by hand.
	const nasty = `p@ss/w0rd:#?`
	p, err = ResolveProxy("proxy.corp:3128", "bob:"+nasty)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = p.Fn(req)
	if got, _ := u.User.Password(); got != nasty {
		t.Errorf("password round-trip = %q, want %q", got, nasty)
	}
	if u.Host != "proxy.corp:3128" {
		t.Errorf("host mangled by the password: %q", u.Host)
	}

	if _, err := ResolveProxy("http://proxy.corp:3128", "nocolon"); err == nil {
		t.Error("--proxy-creds without a colon must fail")
	}
	// Credentials with no proxy at all are pointless: say so rather than ignore.
	p, _ = ResolveProxy("", "alice:s3cr3t")
	if p.Note == "" {
		t.Error("expected a note when --proxy-creds is given with no proxy")
	}
}

// Credentials embedded in HTTPS_PROXY must also be honoured and redacted.
func TestProxyCredentialsFromEnvironment(t *testing.T) {
	clearProxyEnv(t)
	t.Setenv("HTTPS_PROXY", "http://carol:hunter2@proxy.corp:3128")

	p, err := ResolveProxy("", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.URL, "hunter2") {
		t.Errorf("password from the environment leaked into output: %q", p.URL)
	}
	req, _ := http.NewRequest("GET", "https://ghcr.io/v2/", nil)
	u, _ := p.Fn(req)
	if pass, _ := u.User.Password(); pass != "hunter2" {
		t.Errorf("environment credentials not used: %v", u.User)
	}
}

func clearProxyEnv(t *testing.T) {
	t.Helper()
	for _, n := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "NO_PROXY", "no_proxy"} {
		t.Setenv(n, "")
	}
}

func TestResolveProxyPrecedence(t *testing.T) {
	clearProxyEnv(t)

	// Nothing configured: connect directly, and say so.
	p, err := ResolveProxy("", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Fn != nil || p.URL != "" || p.Describe() != ProxySourceNone {
		t.Errorf("no configuration should mean direct, got %+v", p)
	}

	// The environment is picked up (this is what the user set to unblock themselves).
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7890")
	p, err = ResolveProxy("", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.URL != "http://127.0.0.1:7890" || !strings.Contains(p.Source, "HTTPS_PROXY") {
		t.Errorf("environment proxy not reported: %+v", p)
	}

	// --proxy wins over the environment.
	p, err = ResolveProxy("127.0.0.1:1080", "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Source != ProxySourceFlag || p.URL != "http://127.0.0.1:1080" {
		t.Errorf("--proxy must win: %+v", p)
	}
	req, _ := http.NewRequest("GET", "https://ghcr.io/v2/", nil)
	u, err := p.Fn(req)
	if err != nil || u == nil || u.Host != "127.0.0.1:1080" {
		t.Errorf("proxy func should route through the flag value, got %v (%v)", u, err)
	}

	if _, err := ResolveProxy("://bad", ""); err == nil {
		t.Error("an unparseable --proxy must fail loudly")
	}
}
