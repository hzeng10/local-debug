package k8s

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeInfo is what `ldbg cluster install` needs to know about a node before it
// can push an image into it: where to reach it, which runtime will hold the
// image, and whether it can run workloads at all.
type NodeInfo struct {
	Name       string `json:"name"`
	InternalIP string `json:"internalIP,omitempty"`
	Arch       string `json:"arch,omitempty"`
	OS         string `json:"os,omitempty"`
	// Runtime is the normalized runtime name ("containerd", "docker", "cri-o")
	// taken from containerRuntimeVersion, which the kubelet reports as
	// "<runtime>://<version>".
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	// ProviderID identifies local development clusters (kind://, k3s://, …), for
	// which ldbg can use the cluster tool's own image-load command.
	ProviderID string `json:"providerID,omitempty"`
	// Schedulable is false for cordoned nodes and for nodes whose taints keep
	// ordinary workloads away — importing an image there is usually wasted work.
	Schedulable bool `json:"schedulable"`
}

// Nodes lists the cluster's nodes. Listing nodes is frequently denied to
// developer credentials, so callers should treat an error as "unknown" and fall
// back to explicitly supplied node addresses rather than failing outright.
func (c *Client) Nodes(ctx context.Context) ([]NodeInfo, error) {
	nl, err := c.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	nodes := make([]NodeInfo, 0, len(nl.Items))
	for i := range nl.Items {
		n := &nl.Items[i]
		rt, ver := parseRuntime(n.Status.NodeInfo.ContainerRuntimeVersion)
		nodes = append(nodes, NodeInfo{
			Name:           n.Name,
			InternalIP:     internalIP(n),
			Arch:           n.Status.NodeInfo.Architecture,
			OS:             n.Status.NodeInfo.OperatingSystem,
			Runtime:        rt,
			RuntimeVersion: ver,
			ProviderID:     n.Spec.ProviderID,
			Schedulable:    schedulable(n),
		})
	}
	return nodes, nil
}

// NodeArchitectures counts the cluster nodes by CPU architecture ("amd64" → 3).
// It tells `ldbg bundle` which --platform to build for, so an amd64 laptop doesn't
// silently produce an image the cluster cannot exec.
func (c *Client) NodeArchitectures(ctx context.Context) (map[string]int, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return nil, err
	}
	archs := map[string]int{}
	for _, n := range nodes {
		if n.Arch != "" {
			archs[n.Arch]++
		}
	}
	return archs, nil
}

// parseRuntime splits the kubelet's "docker://29.2.1" / "containerd://1.7.13" /
// "cri-o://1.28.2" into a runtime name and version.
func parseRuntime(v string) (name, version string) {
	name, version, _ = strings.Cut(strings.TrimSpace(v), "://")
	return strings.ToLower(name), version
}

func internalIP(n *corev1.Node) string {
	for _, a := range n.Status.Addresses {
		if a.Type == corev1.NodeInternalIP {
			return a.Address
		}
	}
	return ""
}

// schedulable ignores the taints kubernetes itself adds for transient conditions
// (disk pressure, not-ready, …) — those nodes still hold images and come back.
// Only a cordon or a deliberate NoSchedule taint means "don't bother".
func schedulable(n *corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, t := range n.Spec.Taints {
		if t.Effect == corev1.TaintEffectNoSchedule && !strings.HasPrefix(t.Key, "node.kubernetes.io/") {
			return false
		}
	}
	return true
}
