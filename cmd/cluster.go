package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/offline"
	"github.com/hzeng10/local-debug/internal/tp"
	"github.com/spf13/cobra"
)

var clusterCmd = &cobra.Command{
	Use:   "cluster",
	Short: "Air-gapped cluster operations (offline traffic-manager install)",
}

var (
	clusterRegistry   string
	clusterBundle     string
	clusterImportVia  string // auto|registry|ssh|minikube|kind|k3d|ctr
	clusterAgentImage string
	clusterVersion    string
	clusterNoImport   bool
	clusterDryRun     bool
	clusterImportOnly bool
	// preflight keeps its own copy: sharing the variable made cobra re-default
	// install's --import-via to whichever command registered the flag last.
	clusterPreflightVia string

	// SSH import
	clusterSSHUser     string
	clusterNodes       []string
	clusterSSHOpts     string
	clusterSudo        bool
	clusterRemoteTmp   string
	clusterImportCmd   string
	clusterKeepRemote  bool
	clusterSkipPresent bool

	// registry push
	clusterEngine     string
	clusterCreds      string
	clusterInsecure   bool
	clusterProxy      string
	clusterProxyCreds string
)

// clusterPreflightResult is the --json payload for `ldbg cluster preflight`.
type clusterPreflightResult struct {
	KubernetesVersion string         `json:"kubernetesVersion"`
	Image             string         `json:"image"`
	ImportVia         string         `json:"importVia"`
	Nodes             []k8s.NodeInfo `json:"nodes,omitempty"`
	NodeArchitectures map[string]int `json:"nodeArchitectures,omitempty"`
	NodeRuntimes      map[string]int `json:"nodeRuntimes,omitempty"`
	BundlePlatform    string         `json:"bundlePlatform,omitempty"`
}

var clusterPreflightCmd = &cobra.Command{
	Use:   "preflight",
	Short: "Check the cluster is ready for an offline traffic-manager install",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		cl, err := newK8sClient()
		if err != nil {
			return out.Failf("cluster preflight", "check --kubeconfig/--context", err)
		}
		v, err := cl.Ping(ctx)
		if err != nil {
			return out.Failf("cluster preflight", "", err)
		}
		res := clusterPreflightResult{
			KubernetesVersion: v, Image: offline.ImageFor(clusterVersion), ImportVia: clusterPreflightVia,
		}
		// The nodes decide two things: which --platform `ldbg bundle` must target,
		// and which load command `cluster install` has to run on each node.
		// Listing nodes is frequently denied to developer credentials — degrade.
		nodes, aerr := cl.Nodes(ctx)
		if aerr == nil {
			res.Nodes = nodes
			res.NodeArchitectures, res.NodeRuntimes = map[string]int{}, map[string]int{}
			for _, n := range nodes {
				if n.Arch != "" {
					res.NodeArchitectures[n.Arch]++
				}
				if n.Runtime != "" {
					res.NodeRuntimes[n.Runtime]++
				}
			}
			res.BundlePlatform = dominantPlatform(res.NodeArchitectures)
		}
		out.Result("cluster preflight", renderPreflight(res, aerr), res)
		return nil
	},
}

// dominantPlatform turns the node arch census into the platform to bundle for
// (the most common one); empty when the census is empty.
func dominantPlatform(archs map[string]int) string {
	best, bestN := "", 0
	for a, n := range archs {
		if n > bestN || (n == bestN && a < best) {
			best, bestN = a, n
		}
	}
	if best == "" {
		return ""
	}
	return "linux/" + best
}

func renderPreflight(r clusterPreflightResult, archErr error) string {
	var b strings.Builder
	fmt.Fprintf(&b, "cluster reachable: kubernetes %s\n", r.KubernetesVersion)
	switch {
	case archErr != nil:
		fmt.Fprintf(&b, "nodes: unknown (%v)\n  → pass --platform to 'ldbg bundle' and --nodes to 'cluster install' yourself\n", archErr)
	case len(r.Nodes) > 0:
		fmt.Fprintf(&b, "nodes (%d):\n", len(r.Nodes))
		for _, n := range r.Nodes {
			flag := ""
			if !n.Schedulable {
				flag = "  [unschedulable]"
			}
			fmt.Fprintf(&b, "  %-24s %-10s %-8s %s%s\n",
				n.Name, orUnknown(n.Runtime), orUnknown(n.Arch), n.InternalIP, flag)
		}
	}
	fmt.Fprintf(&b, "target image: %s\n", r.Image)
	if r.BundlePlatform != "" {
		fmt.Fprintf(&b, "→ bundle:  ldbg bundle --platform %s\n", r.BundlePlatform)
		if len(r.NodeArchitectures) > 1 {
			b.WriteString("   (mixed-architecture cluster: one bundle per arch — run bundle once for each)\n")
		}
	}
	fmt.Fprintf(&b, "→ install: %s", installSuggestion(r))
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

// installSuggestion recommends an import route from what the cluster looks like.
func installSuggestion(r clusterPreflightResult) string {
	switch {
	case r.ImportVia != "" && r.ImportVia != "auto":
		return fmt.Sprintf("ldbg cluster install --bundle <tar> --import-via %s", r.ImportVia)
	case len(r.Nodes) == 1 && r.Nodes[0].Name == "minikube":
		return "ldbg cluster install --bundle <tar> --import-via minikube"
	case len(r.Nodes) > 0:
		return "ldbg cluster install --bundle <tar> --registry <host/path>" +
			"        (multi-node: every node pulls from the registry)\n" +
			"           …or: ldbg cluster install --bundle <tar> --ssh-user <user> --dry-run" +
			"   (no registry: copy + load on each node)"
	default:
		return "ldbg cluster install --bundle <tar> --nodes user@ip1,user@ip2   (node list unavailable here)"
	}
}

type clusterInstallResult struct {
	Image        string               `json:"image"`
	ImportVia    string               `json:"importVia"`
	Registry     string               `json:"registry,omitempty"`
	AgentImage   string               `json:"agentImage"`
	Nodes        []offline.NodeResult `json:"nodes,omitempty"`
	FullyCovered bool                 `json:"fullyCovered"`
	DryRun       bool                 `json:"dryRun,omitempty"`
	Installed    bool                 `json:"installed"`
}

var clusterInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Offline-install the traffic-manager (import image + embedded-chart helm install)",
	Long: `install performs the air-gapped traffic-manager install: get the bundled tel2
image into the cluster, then 'telepresence helm install' from the client's embedded
chart with images.registry / images.agentImage pointed at it and pullPolicy
IfNotPresent — so the cluster never reaches the internet.

Import methods (--import-via, default auto):
  registry  push the bundle to an internal registry; every node pulls from there.
            Needs no Docker (--engine docker keeps the old load/tag/push path).
  ssh       copy the bundle to each node and load it into that node's own runtime.
            The runtime (containerd / docker / cri-o) is read from the cluster, so
            the right load command is used per node. Needs SSH and (usually)
            passwordless sudo on the nodes.
  minikube | kind | k3d   single-node development clusters
  auto      registry when --registry is set, ssh when --ssh-user/--nodes is set,
            the cluster tool for a recognised local cluster; otherwise it stops
            and asks rather than guessing.

The injected traffic-agent runs the SAME image as the traffic-manager and follows
the intercepted workload, so every schedulable node needs it: 'ssh' imports into
all of them by default and warns when coverage is partial. Use --dry-run first to
see the exact per-node commands.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		image := offline.ImageFor(clusterVersion)
		res := clusterInstallResult{Image: image, DryRun: clusterDryRun}

		// 1) Get the image into the cluster (unless it is already there).
		if clusterNoImport {
			res.ImportVia, res.FullyCovered = "none", true
		} else {
			via, verr := resolveImportVia(ctx)
			if verr != nil {
				return out.Failf("cluster install", "run 'ldbg cluster preflight' to see the nodes and their runtimes", verr)
			}
			var outcome importOutcome
			var ierr error
			switch via {
			case "ssh":
				outcome, ierr = importViaSSH(ctx, image)
			case "registry":
				outcome, ierr = importViaRegistry(ctx, image)
			case "minikube", "kind", "k3d", "ctr":
				outcome, ierr = importViaClusterTool(ctx, via, image)
			default:
				ierr = fmt.Errorf("unknown --import-via %q", via)
			}
			res.ImportVia, res.Nodes, res.FullyCovered = via, outcome.Nodes, outcome.FullyCovered
			if outcome.Registry != "" {
				res.Registry = outcome.Registry
			}
			if ierr != nil {
				return out.Failf("cluster install", importHint(via), ierr)
			}
			if !res.FullyCovered && via == "ssh" && !clusterDryRun {
				out.Info("! not every schedulable node has the image — an intercepted pod scheduled onto a missing node will ImagePullBackOff")
			}
		}

		// A dry run stops here: nothing was changed, so there is nothing to install.
		if clusterDryRun {
			out.Result("cluster install", "Dry run — nothing was changed.", res)
			return nil
		}
		// --import-only lets the image distribution and the helm install be done by
		// different people (or at different times), and keeps the per-node report
		// in the result even when the cluster half is someone else's job.
		if clusterImportOnly {
			human := "Image imported; skipping the traffic-manager install (--import-only)."
			if t := renderNodes(res.Nodes); t != "" {
				human = strings.TrimRight(t, "\n") + "\n" + human
			}
			out.Result("cluster install", human, res)
			return nil
		}

		// 2) Install the traffic-manager from the embedded chart.
		agentImage := clusterAgentImage
		if agentImage == "" {
			agentImage = image // same tel2 image serves the agent
		}
		registry := clusterRegistry // empty → chart default (ghcr.io/telepresenceio)
		out.Info("… telepresence helm install (embedded chart, pullPolicy=IfNotPresent)")
		tpc := newTPClient()
		if !tpc.Available() {
			return out.Failf("cluster install", "install the telepresence client or pass --telepresence-bin", errTelepresenceMissing)
		}
		hopts := tp.HelmOpts{
			ManagerNamespace: managerNamespace,
			Registry:         registry,
			AgentImage:       agentImage,
			PullPolicy:       "IfNotPresent",
		}
		if err := tpc.HelmInstall(ctx, hopts); err != nil {
			// Re-running install is normal (adding nodes, changing the registry),
			// and agents retry — so an existing release upgrades instead of erroring.
			if !strings.Contains(err.Error(), "already installed") {
				return out.Failf("cluster install", "is the image imported and reachable by the cluster?", err)
			}
			out.Info("… traffic-manager already installed — upgrading it instead")
			if uerr := tpc.HelmUpgrade(ctx, hopts); uerr != nil {
				return out.Failf("cluster install", "is the image imported and reachable by the cluster?", uerr)
			}
		}
		res.Registry, res.AgentImage, res.Installed = registry, agentImage, true
		human := "Traffic-manager installed offline from " + image
		if t := renderNodes(res.Nodes); t != "" {
			human = strings.TrimRight(t, "\n") + "\n" + human
		}
		out.Result("cluster install", human, res)
		return nil
	},
}

// importHint points at the most common cause of failure per method.
func importHint(via string) string {
	switch via {
	case "ssh":
		return "check SSH access to the nodes (--ssh-opts '-i <key>'), and that the login can sudo without a password (or pass --sudo=false); --dry-run shows the exact commands"
	case "registry":
		return "check --registry is reachable and writable (--creds user:password, --insecure for a self-signed one)"
	default:
		return "for ctr/per-node import, follow the printed manual step"
	}
}

func init() {
	pf := clusterCmd.PersistentFlags()
	pf.StringVar(&clusterRegistry, "registry", "", "internal registry path hosting the tel2 image (for --import-via registry)")
	pf.StringVar(&clusterVersion, "tp-version", TelepresenceVersion, "Telepresence version (selects the tel2 image tag)")

	insF := clusterInstallCmd.Flags()
	insF.StringVar(&clusterBundle, "bundle", "tel2-bundle.tar", "transfer bundle produced by 'ldbg bundle'")
	insF.StringVar(&clusterImportVia, "import-via", "auto", "image import method: auto|registry|ssh|minikube|kind|k3d|ctr")
	insF.StringVar(&clusterAgentImage, "agent-image", "", "override traffic-agent image (default: same tel2 image)")
	insF.BoolVar(&clusterNoImport, "no-import", false, "skip image import (already present in the cluster)")
	insF.BoolVar(&clusterDryRun, "dry-run", false, "print the per-node commands that would run, change nothing")
	insF.BoolVar(&clusterImportOnly, "import-only", false, "only get the image into the cluster; skip the traffic-manager install")
	// SSH import
	insF.StringVar(&clusterSSHUser, "ssh-user", "", "login for the cluster nodes (addresses come from the cluster unless --nodes is given)")
	insF.StringSliceVar(&clusterNodes, "nodes", nil, "node addresses to import into, e.g. root@10.0.0.1,root@10.0.0.2 (default: every schedulable node)")
	insF.StringVar(&clusterSSHOpts, "ssh-opts", "", "extra options passed to ssh/scp, e.g. \"-i ~/.ssh/id_rsa -o StrictHostKeyChecking=no\"")
	insF.BoolVar(&clusterSudo, "sudo", true, "run the node's load command through sudo (needs passwordless sudo when non-interactive)")
	insF.StringVar(&clusterRemoteTmp, "remote-tmp", "/tmp", "directory on the node to stage the archive in")
	insF.StringVar(&clusterImportCmd, "import-cmd", "", "override the node's load command (%s = archive path) for unusual runtimes")
	insF.BoolVar(&clusterKeepRemote, "keep-remote", false, "keep the transferred archive on the nodes")
	insF.BoolVar(&clusterSkipPresent, "skip-present", false, "leave nodes that already have the image untouched")
	// registry push
	insF.StringVar(&clusterEngine, "engine", "auto", "registry push engine: auto|native|docker (native needs no Docker)")
	insF.StringVar(&clusterCreds, "creds", "", "registry credentials as user:password")
	insF.BoolVar(&clusterInsecure, "insecure", false, "allow a plain-HTTP / self-signed registry")
	insF.StringVar(&clusterProxy, "proxy", "", "proxy for the registry push, e.g. http://127.0.0.1:7890")
	insF.StringVar(&clusterProxyCreds, "proxy-creds", "", "credentials for that proxy as user:password")

	preF := clusterPreflightCmd.Flags()
	preF.StringVar(&clusterPreflightVia, "import-via", "auto", "intended image import method (only affects the printed suggestion)")

	clusterCmd.AddCommand(clusterPreflightCmd, clusterInstallCmd)
	rootCmd.AddCommand(clusterCmd)
}
