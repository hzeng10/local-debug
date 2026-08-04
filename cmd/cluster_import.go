package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/offline"
)

// importOutcome is what an import method reports back to `cluster install`.
type importOutcome struct {
	Via          string
	Nodes        []offline.NodeResult
	FullyCovered bool
	Registry     string // destination reference, when pushed to a registry
}

// resolveImportVia turns --import-via auto into a concrete method. It never
// guesses a method that touches nodes: SSH needs an explicit signal, because
// logging into every node of a shared cluster is not something to infer.
func resolveImportVia(ctx context.Context) (string, error) {
	if v := strings.TrimSpace(clusterImportVia); v != "" && v != "auto" {
		return v, nil
	}
	if clusterRegistry != "" {
		return "registry", nil
	}
	if len(clusterNodes) > 0 || clusterSSHUser != "" {
		return "ssh", nil
	}
	if local := detectLocalCluster(ctx); local != "" {
		return local, nil
	}
	return "", fmt.Errorf("cannot decide how to import the image; pick one:\n" +
		"  • internal registry : --registry <host/path>            (best for multi-node clusters)\n" +
		"  • SSH to the nodes  : --ssh-user <user>  (or --nodes user@ip1,user@ip2)\n" +
		"  • local dev cluster : --import-via minikube|kind|k3d")
}

// detectLocalCluster recognises a single-node development cluster from its
// provider ID / node name, where the cluster tool loads images itself.
func detectLocalCluster(ctx context.Context) string {
	cl, err := newK8sClient()
	if err != nil {
		return ""
	}
	nodes, err := cl.Nodes(ctx)
	if err != nil || len(nodes) != 1 {
		return ""
	}
	switch n := nodes[0]; {
	case strings.HasPrefix(n.ProviderID, "kind://"):
		return "kind"
	case strings.HasPrefix(n.ProviderID, "k3s://"):
		return "k3d"
	case n.Name == "minikube":
		return "minikube"
	default:
		return ""
	}
}

// importViaSSH copies the bundle to every selected node and loads it into that
// node's own runtime.
func importViaSSH(ctx context.Context, image string) (importOutcome, error) {
	targets, all, err := sshTargets(ctx)
	if err != nil {
		return importOutcome{}, err
	}
	o := offline.SSHOpts{
		User:        clusterSSHUser,
		Opts:        strings.Fields(clusterSSHOpts),
		RemoteTmp:   clusterRemoteTmp,
		Sudo:        clusterSudo,
		ImportCmd:   clusterImportCmd,
		SkipPresent: clusterSkipPresent,
		KeepRemote:  clusterKeepRemote,
		DryRun:      clusterDryRun,
	}

	res := importOutcome{Via: "ssh"}
	for _, t := range targets {
		if clusterDryRun {
			out.Info("• %s (%s, %s)", t.Name, t.Address, t.Runtime)
		} else {
			out.Info("… %s (%s, %s): transferring and importing", t.Name, t.Address, t.Runtime)
		}
		r := offline.TransferAndImport(ctx, t, clusterBundle, image, o)
		if clusterDryRun {
			for _, c := range r.Commands {
				out.Info("    %s", c)
			}
		} else if r.OK() {
			out.Info("  ✓ %s", nodeState(r))
		} else {
			out.Info("  ✗ %s: %s", t.Name, r.Error)
		}
		res.Nodes = append(res.Nodes, r)
	}
	res.FullyCovered = coversAll(res.Nodes, all)
	if !clusterDryRun && !anyOK(res.Nodes) {
		return res, fmt.Errorf("no node received the image")
	}
	return res, nil
}

func nodeState(r offline.NodeResult) string {
	if r.Skipped {
		return r.Node + ": already present"
	}
	return r.Node + ": imported and verified"
}

func anyOK(rs []offline.NodeResult) bool {
	for _, r := range rs {
		if r.OK() {
			return true
		}
	}
	return false
}

// coversAll reports whether every schedulable node ended up with the image. The
// traffic-agent is injected into the intercepted workload, so a node that misses
// the image turns into an ImagePullBackOff the moment a pod lands there.
func coversAll(done []offline.NodeResult, all []k8s.NodeInfo) bool {
	if len(all) == 0 {
		return false // node list unavailable: cannot claim coverage
	}
	ok := map[string]bool{}
	for _, r := range done {
		if r.OK() {
			ok[r.Node] = true
		}
	}
	for _, n := range all {
		if n.Schedulable && !ok[n.Name] {
			return false
		}
	}
	return true
}

// sshTargets builds the per-node SSH targets, and returns the full schedulable
// node list so the caller can report coverage. --nodes overrides discovery but
// still borrows the runtime from the cluster when the node can be matched.
func sshTargets(ctx context.Context) ([]offline.Target, []k8s.NodeInfo, error) {
	var all []k8s.NodeInfo
	if cl, err := newK8sClient(); err == nil {
		all, _ = cl.Nodes(ctx) // optional: --nodes works without list-nodes RBAC
	}

	if len(clusterNodes) > 0 {
		targets := make([]offline.Target, 0, len(clusterNodes))
		for _, spec := range clusterNodes {
			spec = strings.TrimSpace(spec)
			if spec == "" {
				continue
			}
			host := spec
			if _, h, ok := strings.Cut(spec, "@"); ok {
				host = h
			}
			t := offline.Target{Name: host, Address: spec, Runtime: offline.RuntimeUnknown}
			if n := matchNode(all, host); n != nil {
				t.Name, t.Runtime = n.Name, offline.RuntimeOf(n.Runtime)
			} else if clusterImportCmd == "" {
				return nil, nil, fmt.Errorf("node %q is not in the cluster's node list, so its container runtime is unknown — pass --import-cmd with the load command for it", host)
			}
			targets = append(targets, t)
		}
		if len(targets) == 0 {
			return nil, nil, fmt.Errorf("--nodes is empty")
		}
		return targets, all, nil
	}

	if len(all) == 0 {
		return nil, nil, fmt.Errorf("cannot list cluster nodes (RBAC?) — pass the node addresses explicitly with --nodes user@ip1,user@ip2")
	}
	if clusterSSHUser == "" {
		return nil, nil, fmt.Errorf("--ssh-user is required to log in to the nodes (or give full addresses with --nodes user@ip)")
	}
	var targets []offline.Target
	var skipped []string
	for _, n := range all {
		switch {
		case !n.Schedulable:
			skipped = append(skipped, n.Name+" (unschedulable)")
		case n.InternalIP == "":
			skipped = append(skipped, n.Name+" (no InternalIP)")
		default:
			targets = append(targets, offline.Target{
				Name: n.Name, Address: n.InternalIP, Runtime: offline.RuntimeOf(n.Runtime),
			})
		}
	}
	if len(skipped) > 0 {
		sort.Strings(skipped)
		out.Info("! skipping %s", strings.Join(skipped, ", "))
	}
	if len(targets) == 0 {
		return nil, nil, fmt.Errorf("no schedulable node with an InternalIP to import into")
	}
	return targets, all, nil
}

func matchNode(all []k8s.NodeInfo, host string) *k8s.NodeInfo {
	for i := range all {
		if all[i].Name == host || all[i].InternalIP == host {
			return &all[i]
		}
	}
	return nil
}

// importViaRegistry pushes the bundle to an internal registry. The native engine
// needs no Docker, matching `ldbg bundle`; --engine docker keeps the old
// load/tag/push path for anyone with an existing Docker workflow.
func importViaRegistry(ctx context.Context, image string) (importOutcome, error) {
	if clusterRegistry == "" {
		return importOutcome{}, fmt.Errorf("registry import needs --registry <host/path>")
	}
	dst := offline.PushDestination(clusterRegistry, image)
	res := importOutcome{Via: "registry", Registry: dst, FullyCovered: true} // every node pulls from the registry

	if clusterDryRun {
		out.Info("• would push %s → %s", clusterBundle, dst)
		return res, nil
	}
	if clusterEngine == "docker" {
		out.Info("… docker load + tag + push → %s", dst)
		if _, err := offline.ImportBundle(ctx, offline.ImportRegistry, clusterBundle, image, clusterRegistry); err != nil {
			return res, err
		}
		return res, nil
	}
	proxy, err := offline.ResolveProxy(clusterProxy, clusterProxyCreds)
	if err != nil {
		return res, err
	}
	out.Info("… pushing %s → %s (no Docker needed, %s)", clusterBundle, dst, proxy.Describe())
	if _, err := offline.NativePush(ctx, clusterBundle, dst, offline.PushOpts{
		Creds: clusterCreds, Insecure: clusterInsecure, Proxy: proxy,
	}); err != nil {
		return res, err
	}
	return res, nil
}

// importViaClusterTool covers the single-node development clusters, where the
// cluster's own CLI loads the archive.
func importViaClusterTool(ctx context.Context, via, image string) (importOutcome, error) {
	res := importOutcome{Via: via, FullyCovered: true}
	if clusterDryRun {
		out.Info("• would import %s via %s", clusterBundle, via)
		return res, nil
	}
	out.Info("… importing %s via %s", image, via)
	msg, err := offline.ImportBundle(ctx, offline.Importer(via), clusterBundle, image, clusterRegistry)
	if err != nil {
		return res, err
	}
	if msg != "" {
		out.Info("%s", msg)
	}
	return res, nil
}

// renderNodes is the per-node table for human output.
func renderNodes(rs []offline.NodeResult) string {
	if len(rs) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range rs {
		mark := "✗"
		state := r.Error
		switch {
		case r.Skipped:
			mark, state = "✓", "already present"
		case r.OK():
			mark, state = "✓", "imported + verified"
		}
		fmt.Fprintf(&b, "  %s %-24s %-10s %s\n", mark, r.Node, r.Runtime, state)
	}
	return b.String()
}
