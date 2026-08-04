package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func node(name, runtime, arch, ip string, mods ...func(*corev1.Node)) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			NodeInfo: corev1.NodeSystemInfo{
				ContainerRuntimeVersion: runtime,
				Architecture:            arch,
				OperatingSystem:         "linux",
			},
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeHostName, Address: name},
				{Type: corev1.NodeInternalIP, Address: ip},
			},
		},
	}
	for _, m := range mods {
		m(n)
	}
	return n
}

func TestNodesReadsRuntimeAndAddress(t *testing.T) {
	cs := fake.NewSimpleClientset(
		node("cp-1", "containerd://1.7.13", "amd64", "10.0.0.1"),
		node("w-1", "docker://24.0.7", "amd64", "10.0.0.2"),
		node("w-2", "cri-o://1.28.2", "arm64", "10.0.0.3"),
	)
	nodes, err := NewClientFromInterface(cs, "default").Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("got %d nodes", len(nodes))
	}
	byName := map[string]NodeInfo{}
	for _, n := range nodes {
		byName[n.Name] = n
	}
	if got := byName["cp-1"]; got.Runtime != "containerd" || got.RuntimeVersion != "1.7.13" {
		t.Errorf("cp-1 runtime = %q/%q", got.Runtime, got.RuntimeVersion)
	}
	if got := byName["w-1"]; got.Runtime != "docker" || got.InternalIP != "10.0.0.2" {
		t.Errorf("w-1 = %+v", got)
	}
	if got := byName["w-2"]; got.Runtime != "cri-o" || got.Arch != "arm64" {
		t.Errorf("w-2 = %+v", got)
	}
	for _, n := range nodes {
		if !n.Schedulable {
			t.Errorf("%s should be schedulable", n.Name)
		}
	}
}

// A cordoned node, and one with a deliberate NoSchedule taint, must not be
// counted as import targets — but kubernetes' own transient condition taints
// must not disqualify a node that is simply momentarily not-ready.
func TestNodesSchedulability(t *testing.T) {
	cs := fake.NewSimpleClientset(
		node("cordoned", "containerd://1.7.13", "amd64", "10.0.0.1", func(n *corev1.Node) {
			n.Spec.Unschedulable = true
		}),
		node("tainted", "containerd://1.7.13", "amd64", "10.0.0.2", func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "dedicated", Effect: corev1.TaintEffectNoSchedule}}
		}),
		node("transient", "containerd://1.7.13", "amd64", "10.0.0.3", func(n *corev1.Node) {
			n.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/not-ready", Effect: corev1.TaintEffectNoSchedule}}
		}),
	)
	nodes, err := NewClientFromInterface(cs, "default").Nodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"cordoned": false, "tainted": false, "transient": true}
	for _, n := range nodes {
		if n.Schedulable != want[n.Name] {
			t.Errorf("%s schedulable = %v, want %v", n.Name, n.Schedulable, want[n.Name])
		}
	}
}

func TestParseRuntime(t *testing.T) {
	cases := []struct{ in, name, ver string }{
		{"docker://29.2.1", "docker", "29.2.1"},
		{"containerd://1.7.13", "containerd", "1.7.13"},
		{"cri-o://1.28.2", "cri-o", "1.28.2"},
		{"", "", ""},
		{"weird", "weird", ""},
	}
	for _, c := range cases {
		n, v := parseRuntime(c.in)
		if n != c.name || v != c.ver {
			t.Errorf("parseRuntime(%q) = (%q, %q), want (%q, %q)", c.in, n, v, c.name, c.ver)
		}
	}
}

func TestNodeArchitecturesDerivesFromNodes(t *testing.T) {
	cs := fake.NewSimpleClientset(
		node("a", "containerd://1.7.13", "amd64", "10.0.0.1"),
		node("b", "containerd://1.7.13", "amd64", "10.0.0.2"),
		node("c", "containerd://1.7.13", "arm64", "10.0.0.3"),
	)
	archs, err := NewClientFromInterface(cs, "default").NodeArchitectures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if archs["amd64"] != 2 || archs["arm64"] != 1 {
		t.Errorf("archs = %v", archs)
	}
}
