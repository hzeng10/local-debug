package k8s

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

// PortForward is an ephemeral local tunnel to one pod port; Close tears it down.
type PortForward struct {
	LocalPort uint16
	stopCh    chan struct{}
	closeOnce sync.Once
}

// Addr returns the local HTTP base URL of the forwarded port.
func (p *PortForward) Addr() string {
	return fmt.Sprintf("http://127.0.0.1:%d", p.LocalPort)
}

// Close stops the forwarder. Safe to call more than once.
func (p *PortForward) Close() {
	p.closeOnce.Do(func() { close(p.stopCh) })
}

// podForService resolves a Running+Ready pod backing the Service via its
// spec.selector (split out so it can be unit-tested with a fake clientset).
func (c *Client) podForService(ctx context.Context, ns, svc string) (string, error) {
	s, err := c.cs.CoreV1().Services(ns).Get(ctx, svc, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("get service %s/%s: %w", ns, svc, err)
	}
	if len(s.Spec.Selector) == 0 {
		return "", fmt.Errorf("service %s/%s has no selector", ns, svc)
	}
	pods, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(s.Spec.Selector).String(),
	})
	if err != nil {
		return "", fmt.Errorf("list pods for service %s/%s: %w", ns, svc, err)
	}
	for _, p := range pods.Items {
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return p.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no ready pod backing service %s/%s", ns, svc)
}

// PortForwardService forwards an ephemeral local port to remotePort on a pod
// backing ns/svc, mirroring `kubectl port-forward svc/<svc>`. The caller must
// Close() the returned PortForward when done.
func (c *Client) PortForwardService(ctx context.Context, ns, svc string, remotePort int) (*PortForward, error) {
	if c.restCfg == nil {
		return nil, fmt.Errorf("port-forward unavailable: client has no rest.Config")
	}
	pod, err := c.podForService(ctx, ns, svc)
	if err != nil {
		return nil, err
	}

	transport, upgrader, err := spdy.RoundTripperFor(c.restCfg)
	if err != nil {
		return nil, fmt.Errorf("build spdy transport: %w", err)
	}
	url := c.cs.CoreV1().RESTClient().Post().
		Resource("pods").Namespace(ns).Name(pod).SubResource("portforward").URL()
	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, http.MethodPost, url)

	stopCh := make(chan struct{})
	readyCh := make(chan struct{})
	fw, err := portforward.New(dialer, []string{fmt.Sprintf("0:%d", remotePort)}, stopCh, readyCh, io.Discard, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("create port-forward: %w", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- fw.ForwardPorts() }()

	select {
	case <-readyCh:
	case err := <-errCh:
		close(stopCh)
		return nil, fmt.Errorf("port-forward to pod %s/%s: %w", ns, pod, err)
	case <-time.After(10 * time.Second):
		close(stopCh)
		return nil, fmt.Errorf("port-forward to pod %s/%s: not ready after 10s", ns, pod)
	case <-ctx.Done():
		close(stopCh)
		return nil, ctx.Err()
	}
	ports, err := fw.GetPorts()
	if err != nil || len(ports) == 0 {
		close(stopCh)
		return nil, fmt.Errorf("port-forward ports: %w", err)
	}
	return &PortForward{LocalPort: ports[0].Local, stopCh: stopCh}, nil
}
