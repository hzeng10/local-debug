package k8s

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// Istio ambient labels/annotations.
const (
	// DataplaneModeLabel enrolls (ambient) or excludes (none) a namespace or pod.
	DataplaneModeLabel = "istio.io/dataplane-mode"
	DataplaneAmbient   = "ambient"
	DataplaneNone      = "none"

	// OptOutAnnotation marks an opt-out that ldbg applied, so `down` reverts only
	// what `up` set (never a user's pre-existing istio.io/dataplane-mode=none).
	OptOutAnnotation = "ldbg.local-debug/ambient-optout"
)

// NamespaceDataplaneMode returns the istio.io/dataplane-mode label on the namespace
// ("" if unset).
func (c *Client) NamespaceDataplaneMode(ctx context.Context, ns string) (string, error) {
	n, err := c.cs.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get namespace %q: %w", ns, err)
	}
	return n.Labels[DataplaneModeLabel], nil
}

// PodTemplateDataplaneMode returns the istio.io/dataplane-mode label on the workload's
// pod template ("" if unset — meaning it inherits the namespace).
func (w *Workload) PodTemplateDataplaneMode() string {
	return w.podTemplateLabels[DataplaneModeLabel]
}

// LdbgAppliedOptOut reports whether ldbg (not the user) applied the ambient opt-out.
func (w *Workload) LdbgAppliedOptOut() bool {
	return w.podTemplateAnnotations[OptOutAnnotation] == "applied"
}

// SetAmbientOptOut patches the workload's pod template to add
// istio.io/dataplane-mode=none plus the ldbg marker annotation, excluding the pod
// from ztunnel redirection so the telepresence traffic-agent owns the port. This is
// required for a working intercept of an ambient workload.
func (c *Client) SetAmbientOptOut(ctx context.Context, w *Workload) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels":      map[string]interface{}{DataplaneModeLabel: DataplaneNone},
					"annotations": map[string]interface{}{OptOutAnnotation: "applied"},
				},
			},
		},
	}
	return c.patchWorkload(ctx, w, patch)
}

// ClearAmbientOptOut removes the dataplane-mode label and marker annotation that
// SetAmbientOptOut added, restoring the workload to namespace-inherited ambient.
// JSON-merge-patch nulls delete the keys.
func (c *Client) ClearAmbientOptOut(ctx context.Context, w *Workload) error {
	patch := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"labels":      map[string]interface{}{DataplaneModeLabel: nil},
					"annotations": map[string]interface{}{OptOutAnnotation: nil},
				},
			},
		},
	}
	return c.patchWorkload(ctx, w, patch)
}

func (c *Client) patchWorkload(ctx context.Context, w *Workload, patch map[string]interface{}) error {
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	switch w.Kind {
	case "Deployment":
		_, err = c.cs.AppsV1().Deployments(w.Namespace).Patch(ctx, w.Name, types.MergePatchType, body, metav1.PatchOptions{})
	case "StatefulSet":
		_, err = c.cs.AppsV1().StatefulSets(w.Namespace).Patch(ctx, w.Name, types.MergePatchType, body, metav1.PatchOptions{})
	case "DaemonSet":
		_, err = c.cs.AppsV1().DaemonSets(w.Namespace).Patch(ctx, w.Name, types.MergePatchType, body, metav1.PatchOptions{})
	default:
		return fmt.Errorf("cannot patch workload kind %q", w.Kind)
	}
	if err != nil {
		return fmt.Errorf("patch %s %q: %w", w.Kind, w.Name, err)
	}
	return nil
}
