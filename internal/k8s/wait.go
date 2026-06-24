package k8s

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WaitWorkloadAvailable polls until the workload's current generation is fully rolled
// out and available, or timeout elapses. Used after an ambient opt-out patch so the
// new (excluded) pod is ready before the intercept engages.
func (c *Client) WaitWorkloadAvailable(ctx context.Context, w *Workload, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		ready, err := c.workloadReady(ctx, w)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s/%s to become available", w.Kind, w.Name)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Client) workloadReady(ctx context.Context, w *Workload) (bool, error) {
	switch w.Kind {
	case "Deployment":
		d, err := c.cs.AppsV1().Deployments(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("get deployment %q: %w", w.Name, err)
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		st := d.Status
		return st.ObservedGeneration >= d.Generation &&
			st.UpdatedReplicas == desired && st.AvailableReplicas == desired, nil
	case "StatefulSet":
		s, err := c.cs.AppsV1().StatefulSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		desired := int32(1)
		if s.Spec.Replicas != nil {
			desired = *s.Spec.Replicas
		}
		return s.Status.ObservedGeneration >= s.Generation && s.Status.ReadyReplicas == desired, nil
	case "DaemonSet":
		ds, err := c.cs.AppsV1().DaemonSets(w.Namespace).Get(ctx, w.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return ds.Status.ObservedGeneration >= ds.Generation &&
			ds.Status.NumberAvailable == ds.Status.DesiredNumberScheduled, nil
	default:
		return true, nil
	}
}
