package offline

import (
	"strings"
	"testing"
)

// The exact stderr docker produced on the user's machine when the VPN reconnect
// wiped the host's DNS servers. It contains BOTH "failed to authorize" and a
// lookup timeout — classifying it as an auth problem would send people hunting
// for credentials they never needed, so this case pins the ordering.
const dockerDNSErr = `docker pull ghcr.io/telepresenceio/tel2:2.29.0: Error response from daemon: ` +
	`failed to resolve reference "ghcr.io/telepresenceio/tel2:2.29.0": failed to authorize: ` +
	`failed to fetch anonymous token: Get "https://ghcr.io/token?scope=repository%3Atelepresenceio%2Ftel2%3Apull&service=ghcr.io": ` +
	`dial tcp: lookup ghcr.io on 127.0.0.53:53: read udp 127.0.0.1:49842->127.0.0.53:53: i/o timeout`

// What a Windows 11 laptop with no route to ghcr.io actually produced. DNS worked
// (the IP is in the message); the TCP handshake timed out, and winsock words that
// nothing like Unix does.
const windowsConnectTimeout = `pull ghcr.io/telepresenceio/tel2:2.29.0 (linux/amd64): ` +
	`Get "https://ghcr.io/v2/": dial tcp 20.205.243.164:443: connectex: A connection attempt failed ` +
	`because the connected party did not properly respond after a period of time, or established ` +
	`connection failed because connected host has failed to respond.`

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want FailKind
	}{
		{"dns beats auth (the reported failure)", dockerDNSErr, FailDNS},
		{"native resolver timeout", `pull ghcr.io/telepresenceio/tel2:2.29.0 (linux/arm64): Get "https://ghcr.io/v2/": dial tcp: lookup ghcr.io: i/o timeout`, FailDNS},
		{"no such host", `Get "https://harbor.corp/v2/": dial tcp: lookup harbor.corp: no such host`, FailDNS},
		{"real auth failure", `GET https://ghcr.io/token...: UNAUTHORIZED: authentication required`, FailAuth},
		{"registry denied", `denied: requested access to the resource is denied`, FailAuth},
		{"missing platform", `no child with platform linux/arm64 in index ghcr.io/telepresenceio/tel2:2.29.0`, FailNotFound},
		{"missing tag", `Error response from daemon: manifest unknown`, FailNotFound},
		{"self-signed registry", `x509: certificate signed by unknown authority`, FailTLS},
		{"daemon down", `Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?`, FailDaemon},
		{"docker missing", `exec: "docker": executable file not found in $PATH`, FailDaemon},
		{"registry unreachable", `Get "https://harbor.corp/v2/": dial tcp 10.0.0.9:443: connect: connection refused`, FailNet},
		{"blocked route", `dial tcp 140.82.116.33:443: connect: no route to host`, FailNet},
		// Windows phrases socket errors entirely differently — matching only the
		// Unix strings dropped every Windows network failure into "unknown".
		{"windows connect timeout (reported from Win11)", windowsConnectTimeout, FailNet},
		{"windows connection refused", `dial tcp 10.0.0.9:443: connectex: No connection could be made because the target machine actively refused it.`, FailNet},
		{"unrecognised", `something else entirely`, FailUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.in); got != c.want {
				t.Fatalf("Classify() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestHintForDNSOffersBothEscapeHatches(t *testing.T) {
	h := HintFor(FailDNS, ImageFor("2.29.0"), DefaultPlatform, "docker", true)
	for _, want := range []string{"--no-pull", "--from"} {
		if !strings.Contains(h, want) {
			t.Errorf("DNS hint %q must mention %s", h, want)
		}
	}
	// Without docker there is no local image to fall back on.
	if h := HintFor(FailDNS, ImageFor("2.29.0"), DefaultPlatform, "native", false); strings.Contains(h, "--no-pull") {
		t.Errorf("hint should not suggest --no-pull when docker is absent: %q", h)
	}
	if h := HintFor(FailDaemon, "", "", "docker", false); !strings.Contains(h, "--engine native") {
		t.Errorf("daemon hint must point at the daemon-free engine: %q", h)
	}
}

// A hint that tells you to use the engine you are already using is noise; that is
// exactly what the Windows report showed ("--engine native" while on native).
func TestHintNeverSuggestsTheEngineInUse(t *testing.T) {
	img := ImageFor("2.29.0")
	for _, kind := range []FailKind{FailNet, FailUnknown} {
		if h := HintFor(kind, img, DefaultPlatform, "native", false); strings.Contains(h, "--engine native") {
			t.Errorf("%s hint suggests the engine already in use: %q", kind, h)
		}
		if h := HintFor(kind, img, DefaultPlatform, "docker", true); strings.Contains(h, "--engine docker") {
			t.Errorf("%s hint suggests the engine already in use: %q", kind, h)
		}
		// The other engine is worth offering when it exists.
		if h := HintFor(kind, img, DefaultPlatform, "docker", true); !strings.Contains(h, "--engine native") {
			t.Errorf("%s hint should offer the daemon-free engine: %q", kind, h)
		}
	}
	// Every unreachable-registry hint must name the mirror escape hatch.
	if h := HintFor(FailNet, img, DefaultPlatform, "native", false); !strings.Contains(h, "--from") {
		t.Errorf("net hint must offer --from: %q", h)
	}
}

func TestValidPlatform(t *testing.T) {
	for _, ok := range []string{"linux/amd64", "linux/arm64", "linux/arm64/v8"} {
		if err := ValidPlatform(ok); err != nil {
			t.Errorf("ValidPlatform(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "amd64", "linux/", "linux/amd64/v8/extra"} {
		if err := ValidPlatform(bad); err == nil {
			t.Errorf("ValidPlatform(%q) = nil, want error", bad)
		}
	}
	// A comma list must fail loudly: one bundle carries one architecture.
	err := ValidPlatform("linux/amd64,linux/arm64")
	if err == nil {
		t.Fatal("comma-separated platforms must be rejected")
	}
	if !strings.Contains(err.Error(), "one bundle carries one architecture") {
		t.Errorf("error should explain the one-arch rule, got %q", err)
	}
}

func TestSourceImage(t *testing.T) {
	canonical := ImageFor("2.29.0")
	cases := []struct{ from, want string }{
		{"", canonical},
		{"harbor.corp/mirror", "harbor.corp/mirror/tel2:2.29.0"},
		{"harbor.corp/mirror/", "harbor.corp/mirror/tel2:2.29.0"},
		{"harbor.corp/x/tel2:2.29.0", "harbor.corp/x/tel2:2.29.0"},
		{"registry.local:5000/mirror", "registry.local:5000/mirror/tel2:2.29.0"},
		// A bare host:port is a registry prefix, not a tagged reference.
		{"registry.local:5000", "registry.local:5000/tel2:2.29.0"},
	}
	for _, c := range cases {
		if got := SourceImage(canonical, c.from); got != c.want {
			t.Errorf("SourceImage(%q) = %q, want %q", c.from, got, c.want)
		}
	}
}

func TestDefaultOutPath(t *testing.T) {
	if got := DefaultOutPath(DefaultPlatform); got != "tel2-bundle.tar" {
		t.Errorf("default platform keeps the documented name, got %q", got)
	}
	if got := DefaultOutPath("linux/arm64"); got != "tel2-bundle-arm64.tar" {
		t.Errorf("arm64 bundle must not overwrite the amd64 one, got %q", got)
	}
	if got := DefaultOutPath("linux/arm64/v8"); got != "tel2-bundle-arm64.tar" {
		t.Errorf("variant is dropped from the name, got %q", got)
	}
}

func TestImageFor(t *testing.T) {
	for _, v := range []string{"2.29.0", "v2.29.0"} {
		if got := ImageFor(v); got != "ghcr.io/telepresenceio/tel2:2.29.0" {
			t.Errorf("ImageFor(%q) = %q", v, got)
		}
	}
}
