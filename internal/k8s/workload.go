package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Workload is a resolved intercept target (a Deployment/StatefulSet/DaemonSet, or a
// workload found via a Service's selector).
type Workload struct {
	Namespace string         `json:"namespace"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	PodSpec   corev1.PodSpec `json:"-"`
	// ServicePorts is populated when the target was resolved via a Service, so up/
	// intercept can default the remote port without the user specifying it.
	ServicePorts []corev1.ServicePort `json:"-"`

	// pod template metadata, for ambient (dataplane-mode) detection/patching.
	podTemplateLabels      map[string]string
	podTemplateAnnotations map[string]string
}

func newWorkload(ns, kind, name string, tmpl corev1.PodTemplateSpec) *Workload {
	return &Workload{
		Namespace: ns, Kind: kind, Name: name, PodSpec: tmpl.Spec,
		podTemplateLabels:      tmpl.Labels,
		podTemplateAnnotations: tmpl.Annotations,
	}
}

// ResolveWorkload finds the workload named `name` in `ns`. It first tries a Service
// of that name (the common case: users think in terms of the Service they call),
// falling back to a same-named Deployment/StatefulSet/DaemonSet.
func (c *Client) ResolveWorkload(ctx context.Context, ns, name string) (*Workload, error) {
	if svc, err := c.cs.CoreV1().Services(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		if len(svc.Spec.Selector) == 0 {
			return nil, fmt.Errorf("service %q has no selector (headless/externalName?) — pass the workload name instead", name)
		}
		wl, err := c.workloadForSelector(ctx, ns, svc.Spec.Selector)
		if err != nil {
			return nil, err
		}
		wl.ServicePorts = svc.Spec.Ports
		return wl, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get service %q: %w", name, err)
	}

	// No Service by that name; try workload kinds directly.
	if d, err := c.cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return newWorkload(ns, "Deployment", d.Name, d.Spec.Template), nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get deployment %q: %w", name, err)
	}
	if s, err := c.cs.AppsV1().StatefulSets(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return newWorkload(ns, "StatefulSet", s.Name, s.Spec.Template), nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get statefulset %q: %w", name, err)
	}
	if ds, err := c.cs.AppsV1().DaemonSets(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
		return newWorkload(ns, "DaemonSet", ds.Name, ds.Spec.Template), nil
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("get daemonset %q: %w", name, err)
	}
	return nil, fmt.Errorf("no Service/Deployment/StatefulSet/DaemonSet named %q in namespace %q", name, ns)
}

// workloadForSelector finds the Deployment/StatefulSet/DaemonSet whose pod template
// labels are a superset of the Service selector.
func (c *Client) workloadForSelector(ctx context.Context, ns string, sel map[string]string) (*Workload, error) {
	deps, err := c.cs.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for i := range deps.Items {
		d := &deps.Items[i]
		if labelsContain(d.Spec.Template.Labels, sel) {
			return newWorkload(ns, "Deployment", d.Name, d.Spec.Template), nil
		}
	}
	sts, err := c.cs.AppsV1().StatefulSets(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets: %w", err)
	}
	for i := range sts.Items {
		s := &sts.Items[i]
		if labelsContain(s.Spec.Template.Labels, sel) {
			return newWorkload(ns, "StatefulSet", s.Name, s.Spec.Template), nil
		}
	}
	return nil, fmt.Errorf("no workload pod template matches service selector %v in namespace %q", sel, ns)
}

// PrimaryContainer returns the container to intercept: the first one whose name
// matches the workload name, else the first container.
func (w *Workload) PrimaryContainer() (corev1.Container, error) {
	if len(w.PodSpec.Containers) == 0 {
		return corev1.Container{}, fmt.Errorf("workload %s/%s has no containers", w.Namespace, w.Name)
	}
	for _, ctr := range w.PodSpec.Containers {
		if ctr.Name == w.Name {
			return ctr, nil
		}
	}
	return w.PodSpec.Containers[0], nil
}

func labelsContain(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return len(want) > 0
}
