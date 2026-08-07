package k8s

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// ManagerScope answers "does the traffic-manager manage this namespace?". The
// question matters because the answer defaults to NO for kube-system: the
// telepresence-oss chart's default selector is
// `kubernetes.io/metadata.name NotIn [kube-system, kube-node-lease]` — so a
// manager can live IN kube-system and still not manage it. A namespace outside
// the manager's scope cannot be intercepted at all: the client refuses with
// "namespace X is not mapped", and the injection webhook would not fire there
// anyway.
type ManagerScope struct {
	// Found is false when the manager's ConfigMap could not be read (not
	// installed, or RBAC) — the caller should skip the check, not fail.
	Found bool `json:"found"`
	// Manages reports whether the selector matches the target namespace.
	Manages bool `json:"manages"`
	// SelectorYAML is the manager's own namespace-selector.yaml, verbatim, so a
	// failure can show what the manager actually said instead of a guess.
	SelectorYAML string `json:"selectorYaml,omitempty"`
}

// managerConfigMap is where the traffic-manager records its state; the chart
// writes the managed-namespace selector into this key.
const (
	managerConfigMap   = "traffic-manager"
	managerSelectorKey = "namespace-selector.yaml"
	metadataNameLabel  = "kubernetes.io/metadata.name"
)

// ManagerScopeFor reads the manager's namespace selector from its ConfigMap in
// managerNS and evaluates it against targetNS's labels.
func (c *Client) ManagerScopeFor(ctx context.Context, managerNS, targetNS string) (ManagerScope, error) {
	cm, err := c.cs.CoreV1().ConfigMaps(managerNS).Get(ctx, managerConfigMap, metav1.GetOptions{})
	if err != nil {
		return ManagerScope{}, nil // not installed there, or no RBAC: unknown, not an error
	}
	raw, ok := cm.Data[managerSelectorKey]
	if !ok || strings.TrimSpace(raw) == "" {
		// The ConfigMap exists but carries no selector — nothing to evaluate.
		return ManagerScope{}, nil
	}

	var sel metav1.LabelSelector
	if err := yaml.Unmarshal([]byte(raw), &sel); err != nil {
		return ManagerScope{Found: true, SelectorYAML: raw}, fmt.Errorf("parse the manager's namespace selector: %w", err)
	}
	s, err := metav1.LabelSelectorAsSelector(&sel)
	if err != nil {
		return ManagerScope{Found: true, SelectorYAML: raw}, fmt.Errorf("invalid namespace selector: %w", err)
	}

	nsLabels := map[string]string{metadataNameLabel: targetNS}
	if ns, err := c.cs.CoreV1().Namespaces().Get(ctx, targetNS, metav1.GetOptions{}); err == nil {
		nsLabels = ns.Labels
		if nsLabels == nil {
			nsLabels = map[string]string{}
		}
		// Every namespace carries this label since k8s 1.21, but a degraded
		// fake/old cluster might not — the name-based selector must still work.
		if _, ok := nsLabels[metadataNameLabel]; !ok {
			nsLabels[metadataNameLabel] = targetNS
		}
	}

	return ManagerScope{Found: true, Manages: s.Matches(labels.Set(nsLabels)), SelectorYAML: raw}, nil
}
