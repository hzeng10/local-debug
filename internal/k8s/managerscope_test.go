package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The chart's DEFAULT selector excludes kube-system — so a manager living in
// kube-system does not manage kube-system. That inversion is exactly what made
// the real-world intercept fail with "namespace kube-system is not mapped",
// and it is why this check exists.
const defaultSelectorYAML = `matchExpressions:
- key: kubernetes.io/metadata.name
  operator: NotIn
  values:
  - kube-system
  - kube-node-lease
`

func nsObj(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: name, Labels: map[string]string{"kubernetes.io/metadata.name": name},
	}}
}

func cmObj(ns, selector string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "traffic-manager", Namespace: ns},
		Data:       map[string]string{"namespace-selector.yaml": selector},
	}
}

func TestManagerScopeFor(t *testing.T) {
	ctx := context.Background()

	cs := fake.NewSimpleClientset(cmObj("kube-system", defaultSelectorYAML), nsObj("kube-system"), nsObj("demo"))
	c := &Client{cs: cs}

	// The default selector: manages demo, does NOT manage kube-system.
	got, err := c.ManagerScopeFor(ctx, "kube-system", "kube-system")
	if err != nil || !got.Found || got.Manages {
		t.Errorf("default selector must exclude kube-system: %+v, %v", got, err)
	}
	if got.SelectorYAML == "" {
		t.Error("the manager's own selector text must be kept for the error message")
	}
	got, err = c.ManagerScopeFor(ctx, "kube-system", "demo")
	if err != nil || !got.Found || !got.Manages {
		t.Errorf("default selector must include demo: %+v, %v", got, err)
	}

	// The `namespaces={kube-system}` install renders an In selector.
	cs = fake.NewSimpleClientset(cmObj("kube-system", "matchExpressions:\n- key: kubernetes.io/metadata.name\n  operator: In\n  values:\n  - kube-system\n"), nsObj("kube-system"), nsObj("demo"))
	c = &Client{cs: cs}
	if got, _ := c.ManagerScopeFor(ctx, "kube-system", "kube-system"); !got.Manages {
		t.Errorf("In selector must include kube-system: %+v", got)
	}
	if got, _ := c.ManagerScopeFor(ctx, "kube-system", "demo"); got.Manages {
		t.Errorf("In selector must exclude demo: %+v", got)
	}

	// matchLabels form.
	cs = fake.NewSimpleClientset(cmObj("ambassador", "matchLabels:\n  team: payments\n"), nsObj("labeled"))
	c = &Client{cs: cs}
	nsLabeled, _ := cs.CoreV1().Namespaces().Get(ctx, "labeled", metav1.GetOptions{})
	nsLabeled.Labels["team"] = "payments"
	_, _ = cs.CoreV1().Namespaces().Update(ctx, nsLabeled, metav1.UpdateOptions{})
	if got, _ := c.ManagerScopeFor(ctx, "ambassador", "labeled"); !got.Manages {
		t.Errorf("matchLabels selector must match the labeled namespace: %+v", got)
	}

	// No ConfigMap (not installed / RBAC): unknown, never an error — the caller
	// degrades to not checking rather than blocking the intercept.
	c = &Client{cs: fake.NewSimpleClientset(nsObj("demo"))}
	if got, err := c.ManagerScopeFor(ctx, "ambassador", "demo"); err != nil || got.Found {
		t.Errorf("missing ConfigMap must be Found=false, no error: %+v, %v", got, err)
	}

	// A selector that does not parse is an error (something is wrong, say so).
	c = &Client{cs: fake.NewSimpleClientset(cmObj("ambassador", ":::not yaml"))}
	if _, err := c.ManagerScopeFor(ctx, "ambassador", "demo"); err == nil {
		t.Error("garbage selector must error")
	}

	// A namespace the client cannot read still evaluates via the name label.
	c = &Client{cs: fake.NewSimpleClientset(cmObj("kube-system", defaultSelectorYAML))}
	if got, _ := c.ManagerScopeFor(ctx, "kube-system", "unreadable-ns"); !got.Found || !got.Manages {
		t.Errorf("name-only evaluation must work without namespace read access: %+v", got)
	}
}
