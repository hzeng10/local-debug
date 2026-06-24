package k8s

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ProbeResult is the outcome of an in-cluster HTTP probe.
type ProbeResult struct {
	URL       string `json:"url"`
	Body      string `json:"body"`
	ExitCode  int32  `json:"exitCode"`
	Succeeded bool   `json:"succeeded"`
}

// ProbeFromCluster runs a short-lived curl Pod inside the cluster that GETs url, so
// the request originates from a real in-cluster client (the only way to prove a
// global intercept actually takes over cluster traffic — a request from the laptop
// would just loop back). The Pod is always deleted before returning.
func (c *Client) ProbeFromCluster(ctx context.Context, ns, url, image string, timeout time.Duration) (*ProbeResult, error) {
	if image == "" {
		image = "curlimages/curl:8.10.1"
	}
	name := fmt.Sprintf("ldbg-probe-%d", time.Now().UnixNano()%1000000)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: map[string]string{"app.kubernetes.io/managed-by": "ldbg"}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{{
				Name:    "curl",
				Image:   image,
				Command: []string{"curl", "-sS", "-m", "12", url},
			}},
		},
	}
	if _, err := c.cs.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create probe pod: %w", err)
	}
	defer func() {
		_ = c.cs.CoreV1().Pods(ns).Delete(context.Background(), name, metav1.DeleteOptions{})
	}()

	res := &ProbeResult{URL: url}
	deadline := time.Now().Add(timeout)
	for {
		p, err := c.cs.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get probe pod: %w", err)
		}
		phase := p.Status.Phase
		if phase == corev1.PodSucceeded || phase == corev1.PodFailed {
			res.Succeeded = phase == corev1.PodSucceeded
			if len(p.Status.ContainerStatuses) > 0 && p.Status.ContainerStatuses[0].State.Terminated != nil {
				res.ExitCode = p.Status.ContainerStatuses[0].State.Terminated.ExitCode
			}
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("probe pod did not complete within %s (phase=%s)", timeout, phase)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	body, _ := c.podLogs(ctx, ns, name)
	res.Body = body
	return res, nil
}

func (c *Client) podLogs(ctx context.Context, ns, name string) (string, error) {
	rc, err := c.cs.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{}).Stream(ctx)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	return string(b), err
}
