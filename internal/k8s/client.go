// Package k8s resolves the cluster-side facts ldbg needs: the target workload and
// the environment a developer must reproduce on the laptop (env, envFrom, and the
// referenced ConfigMaps/Secrets). It uses client-go so it works against any cluster
// the user's kubeconfig can reach.
package k8s

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Client is a thin wrapper over a client-go clientset plus the namespace resolved
// from the active kube-context.
type Client struct {
	cs        kubernetes.Interface
	namespace string
	restCfg   *rest.Config // nil for fake clients; required only by PortForwardService
}

// NewClient builds a Client from a kubeconfig path (empty = default discovery) and
// an optional context name (empty = current-context).
func NewClient(kubeconfigPath, contextName string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfigPath != "" {
		rules.ExplicitPath = kubeconfigPath
	}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restCfg, err := cc.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, fmt.Errorf("build clientset: %w", err)
	}
	ns, _, err := cc.Namespace()
	if err != nil {
		ns = "default"
	}
	return &Client{cs: cs, namespace: ns, restCfg: restCfg}, nil
}

// NewClientFromInterface wraps a pre-built clientset (used by tests with a fake).
func NewClientFromInterface(cs kubernetes.Interface, namespace string) *Client {
	return &Client{cs: cs, namespace: namespace}
}

// DefaultNamespace is the namespace from the active context (used when --namespace
// is not given).
func (c *Client) DefaultNamespace() string { return c.namespace }

// Ping verifies the cluster is reachable and RBAC lets us read the server version.
func (c *Client) Ping(ctx context.Context) (string, error) {
	v, err := c.cs.Discovery().ServerVersion()
	if err != nil {
		return "", fmt.Errorf("cluster unreachable or RBAC denied: %w", err)
	}
	return v.GitVersion, nil
}

// ProbeReadPods lists at most one pod in ns to confirm the current credentials
// have namespaced read RBAC — a representative check for what ldbg actually reads
// (pods, services, workloads). It returns the number of pods in the capped list.
func (c *Client) ProbeReadPods(ctx context.Context, ns string) (int, error) {
	pl, err := c.cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return 0, err
	}
	return len(pl.Items), nil
}
