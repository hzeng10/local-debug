package tp

import (
	"strings"
	"testing"
)

// These value names come from the telepresence-oss chart, whose schema sets
// additionalProperties=false. Any key the chart does not declare makes the whole
// install fail validation before anything is applied — which is exactly what the
// older `images.*` names did against 2.29.0 ("additional properties 'images' not
// allowed"). So the names are pinned here, including the negative case.
func TestHelmSetArgsUsesChartValueNames(t *testing.T) {
	args, err := helmSetArgs(HelmOpts{
		ManagerNamespace: "ambassador",
		AgentImage:       "ghcr.io/telepresenceio/tel2:2.29.0",
		PullPolicy:       "IfNotPresent",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{
		"--namespace ambassador",
		"image.pullPolicy=IfNotPresent",
		"agent.image.registry=ghcr.io/telepresenceio",
		"agent.image.name=tel2",
		"agent.image.tag=2.29.0",
		"agent.image.pullPolicy=IfNotPresent",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	// The chart has no `images` key at any level; emitting one is the regression.
	if strings.Contains(got, "images.") {
		t.Errorf("no value may live under `images.`: %q", got)
	}
}

// An internal registry has to reach both images: the manager pulls its own, and
// the intercepted workload's pod pulls the injected agent's.
func TestHelmSetArgsRegistryCoversBothImages(t *testing.T) {
	args, err := helmSetArgs(HelmOpts{Registry: "harbor.corp/tel", PullPolicy: "IfNotPresent"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	for _, want := range []string{"image.registry=harbor.corp/tel", "agent.image.registry=harbor.corp/tel"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}

	// An explicit agent image wins over the registry default.
	args, err = helmSetArgs(HelmOpts{Registry: "harbor.corp/tel", AgentImage: "other.corp/x/tel2:2.29.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(args, " "); !strings.Contains(got, "agent.image.registry=other.corp/x") ||
		strings.Contains(got, "agent.image.registry=harbor.corp/tel") {
		t.Errorf("explicit agent image must win: %q", got)
	}

	// Nothing set at all: no image values, so the chart's own defaults apply.
	args, _ = helmSetArgs(HelmOpts{ManagerNamespace: "ambassador"})
	if got := strings.Join(args, " "); strings.Contains(got, "image") {
		t.Errorf("no image values expected: %q", got)
	}
}

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ ref, reg, name, tag string }{
		{"ghcr.io/telepresenceio/tel2:2.29.0", "ghcr.io/telepresenceio", "tel2", "2.29.0"},
		// A registry host may carry a port, so only the last segment's colon
		// separates the tag.
		{"harbor.corp:5000/tel/tel2:2.29.0", "harbor.corp:5000/tel", "tel2", "2.29.0"},
		{"tel2:2.29.0", "", "tel2", "2.29.0"},
		{"ghcr.io/telepresenceio/tel2", "ghcr.io/telepresenceio", "tel2", ""},
	}
	for _, c := range cases {
		reg, name, tag, err := splitImageRef(c.ref)
		if err != nil {
			t.Errorf("splitImageRef(%q): %v", c.ref, err)
			continue
		}
		if reg != c.reg || name != c.name || tag != c.tag {
			t.Errorf("splitImageRef(%q) = (%q,%q,%q), want (%q,%q,%q)", c.ref, reg, name, tag, c.reg, c.name, c.tag)
		}
	}
	// The chart assembles registry/name:tag, so a digest cannot be expressed —
	// failing loudly beats installing an agent that pulls something else.
	if _, _, _, err := splitImageRef("ghcr.io/telepresenceio/tel2@sha256:abc123"); err == nil {
		t.Error("a digest reference must be rejected")
	}
	if _, _, _, err := splitImageRef(""); err == nil {
		t.Error("an empty reference must be rejected")
	}
}

// The chart's default selector excludes kube-system, so managing it must be
// requestable at install time; the {} list form is helm's strvals syntax for a
// list value (verified against a live cluster: it renders an In selector).
func TestHelmSetArgsManagedNamespaces(t *testing.T) {
	args, err := helmSetArgs(HelmOpts{ManagedNamespaces: []string{"kube-system", "demo"}, PullPolicy: "IfNotPresent"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	if !strings.Contains(got, "namespaces={kube-system,demo}") {
		t.Errorf("missing namespaces list in %q", got)
	}
	// Unset means unset: the chart's own default selector applies.
	args, _ = helmSetArgs(HelmOpts{PullPolicy: "IfNotPresent"})
	if got := strings.Join(args, " "); strings.Contains(got, "namespaces") {
		t.Errorf("no namespaces value expected: %q", got)
	}
}
