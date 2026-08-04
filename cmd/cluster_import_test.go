package cmd

import (
	"testing"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/offline"
)

// The injected traffic-agent runs the same image and follows the intercepted
// workload, so "covered" means every schedulable node — anything less is an
// ImagePullBackOff waiting to happen and must be reported as partial.
func TestCoversAll(t *testing.T) {
	nodes := []k8s.NodeInfo{
		{Name: "cp-1", Schedulable: true},
		{Name: "w-1", Schedulable: true},
		{Name: "w-2", Schedulable: false}, // cordoned: not required
	}
	ok := func(n string) offline.NodeResult {
		return offline.NodeResult{Node: n, Verified: true}
	}

	if !coversAll([]offline.NodeResult{ok("cp-1"), ok("w-1")}, nodes) {
		t.Error("all schedulable nodes imported should count as full coverage")
	}
	// A node that was skipped because the image is already there still counts.
	if !coversAll([]offline.NodeResult{ok("cp-1"), {Node: "w-1", Skipped: true}}, nodes) {
		t.Error("an already-present node counts as covered")
	}
	if coversAll([]offline.NodeResult{ok("cp-1")}, nodes) {
		t.Error("a missing schedulable node must count as partial coverage")
	}
	// A node that errored is not covered even though it was attempted.
	if coversAll([]offline.NodeResult{ok("cp-1"), {Node: "w-1", Error: "ssh: connection refused"}}, nodes) {
		t.Error("a failed node must count as partial coverage")
	}
	// Without a node list, coverage cannot be claimed.
	if coversAll([]offline.NodeResult{ok("cp-1")}, nil) {
		t.Error("unknown node list must not report full coverage")
	}
}

func TestNodeResultOK(t *testing.T) {
	cases := []struct {
		r    offline.NodeResult
		want bool
	}{
		{offline.NodeResult{Verified: true}, true},
		{offline.NodeResult{Skipped: true}, true},
		// Imported without verification happens only for an unknown runtime
		// driven by --import-cmd; a failed verification sets Error instead.
		{offline.NodeResult{Imported: true}, true},
		{offline.NodeResult{Verified: true, Error: "boom"}, false},
		{offline.NodeResult{}, false},
	}
	for i, c := range cases {
		if got := c.r.OK(); got != c.want {
			t.Errorf("case %d: OK() = %v, want %v (%+v)", i, got, c.want, c.r)
		}
	}
}

func TestMatchNode(t *testing.T) {
	all := []k8s.NodeInfo{
		{Name: "cp-1", InternalIP: "10.0.0.1", Runtime: "containerd"},
		{Name: "w-1", InternalIP: "10.0.0.2", Runtime: "docker"},
	}
	if n := matchNode(all, "10.0.0.2"); n == nil || n.Runtime != "docker" {
		t.Errorf("match by IP failed: %+v", n)
	}
	if n := matchNode(all, "cp-1"); n == nil || n.Runtime != "containerd" {
		t.Errorf("match by name failed: %+v", n)
	}
	if n := matchNode(all, "10.9.9.9"); n != nil {
		t.Errorf("unknown host should not match: %+v", n)
	}
}
