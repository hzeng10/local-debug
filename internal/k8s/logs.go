package k8s

import (
	"bufio"
	"context"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

// PodRef is a pod and its container names (the traffic-agent shows up here when a
// workload is intercepted — its logs reveal whether traffic is reaching the laptop).
type PodRef struct {
	Name       string
	Containers []string
}

// PodsForWorkload lists the pods belonging to a workload, selected by its pod-template
// labels.
func (c *Client) PodsForWorkload(ctx context.Context, w *Workload) ([]PodRef, error) {
	sel := labels.SelectorFromSet(w.podTemplateLabels).String()
	if sel == "" {
		return nil, fmt.Errorf("workload %s/%s has no pod labels to select on", w.Namespace, w.Name)
	}
	list, err := c.cs.CoreV1().Pods(w.Namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	refs := make([]PodRef, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		cs := make([]string, 0, len(p.Spec.Containers))
		for _, ct := range p.Spec.Containers {
			cs = append(cs, ct.Name)
		}
		refs = append(refs, PodRef{Name: p.Name, Containers: cs})
	}
	return refs, nil
}

// ManagerPod returns the name of a traffic-manager pod in the given namespace.
func (c *Client) ManagerPod(ctx context.Context, ns string) (string, error) {
	list, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=traffic-manager"})
	if err != nil {
		return "", fmt.Errorf("list traffic-manager pods: %w", err)
	}
	if len(list.Items) == 0 {
		return "", fmt.Errorf("no traffic-manager pod in namespace %q", ns)
	}
	return list.Items[0].Name, nil
}

// StreamPodLogs copies a container's logs to w, one line per record prefixed with
// prefix. With follow=true it blocks until the stream ends or ctx is cancelled.
func (c *Client) StreamPodLogs(ctx context.Context, ns, pod, container, prefix string, follow bool, tail int64, w io.Writer) error {
	opts := &corev1.PodLogOptions{Container: container, Follow: follow}
	if tail >= 0 {
		opts.TailLines = &tail
	}
	rc, err := c.cs.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return fmt.Errorf("stream logs %s/%s: %w", pod, container, err)
	}
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		fmt.Fprintf(w, "%s %s\n", prefix, sc.Text())
	}
	return sc.Err()
}
