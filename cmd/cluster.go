package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

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
	clusterImportVia  string // registry|minikube|kind|k3d|ctr
	clusterAgentImage string
	clusterVersion    string
	clusterNoImport   bool
)

// clusterPreflightResult is the --json payload for `ldbg cluster preflight`.
type clusterPreflightResult struct {
	KubernetesVersion string         `json:"kubernetesVersion"`
	Image             string         `json:"image"`
	ImportVia         string         `json:"importVia"`
	NodeArchitectures map[string]int `json:"nodeArchitectures,omitempty"`
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
			KubernetesVersion: v, Image: offline.ImageFor(clusterVersion), ImportVia: clusterImportVia,
		}
		// Node architecture decides which --platform `ldbg bundle` must target.
		// Listing nodes is frequently denied to developer credentials — degrade.
		archs, aerr := cl.NodeArchitectures(ctx)
		if aerr == nil {
			res.NodeArchitectures = archs
			res.BundlePlatform = dominantPlatform(archs)
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
		fmt.Fprintf(&b, "node architectures: unknown (%v) — pass --platform to 'ldbg bundle' yourself\n", archErr)
	case len(r.NodeArchitectures) > 0:
		names := make([]string, 0, len(r.NodeArchitectures))
		for a := range r.NodeArchitectures {
			names = append(names, a)
		}
		sort.Strings(names)
		parts := make([]string, 0, len(names))
		for _, a := range names {
			parts = append(parts, fmt.Sprintf("%s×%d", a, r.NodeArchitectures[a]))
		}
		fmt.Fprintf(&b, "node architectures: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(&b, "target image: %s\nimport via: %s\n", r.Image, r.ImportVia)
	if r.BundlePlatform != "" {
		fmt.Fprintf(&b, "→ bundle:  ldbg bundle --platform %s\n", r.BundlePlatform)
		if len(r.NodeArchitectures) > 1 {
			b.WriteString("   (mixed-architecture cluster: one bundle per arch — run bundle once for each)\n")
		}
	}
	fmt.Fprintf(&b, "→ install: ldbg cluster install --bundle <tar> --import-via %s", r.ImportVia)
	return b.String()
}

type clusterInstallResult struct {
	Image      string `json:"image"`
	ImportVia  string `json:"importVia"`
	Registry   string `json:"registry,omitempty"`
	AgentImage string `json:"agentImage"`
	Installed  bool   `json:"installed"`
}

var clusterInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Offline-install the traffic-manager (import image + embedded-chart helm install)",
	Long: `install performs the air-gapped traffic-manager install: import the bundled tel2
image into the cluster (internal registry, or minikube/kind/k3d/ctr node import), then
'telepresence helm install' from the client's embedded chart with images.registry /
images.agentImage pointed at the imported image and pullPolicy IfNotPresent — so the
cluster never reaches the internet.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		image := offline.ImageFor(clusterVersion)

		// 1) Import the image into the cluster (unless already present).
		if !clusterNoImport {
			out.Info("… importing %s via %s", image, clusterImportVia)
			if msg, err := offline.ImportBundle(ctx, offline.Importer(clusterImportVia), clusterBundle, image, clusterRegistry); err != nil {
				return out.Failf("cluster install", "for ctr/per-node import, follow the printed manual step", err)
			} else if msg != "" {
				out.Info("%s", msg)
			}
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
		if err := tpc.HelmInstall(ctx, tp.HelmOpts{
			ManagerNamespace: managerNamespace,
			Registry:         registry,
			AgentImage:       agentImage,
			PullPolicy:       "IfNotPresent",
		}); err != nil {
			return out.Failf("cluster install", "is the image imported and reachable by the cluster?", err)
		}
		res := clusterInstallResult{Image: image, ImportVia: clusterImportVia, Registry: registry, AgentImage: agentImage, Installed: true}
		out.Result("cluster install", "Traffic-manager installed offline from "+image, res)
		return nil
	},
}

func init() {
	pf := clusterCmd.PersistentFlags()
	pf.StringVar(&clusterRegistry, "registry", "", "internal registry path hosting the tel2 image (for --import-via registry)")
	pf.StringVar(&clusterVersion, "tp-version", TelepresenceVersion, "Telepresence version (selects the tel2 image tag)")

	insF := clusterInstallCmd.Flags()
	insF.StringVar(&clusterBundle, "bundle", "tel2-bundle.tar", "transfer bundle produced by 'ldbg bundle'")
	insF.StringVar(&clusterImportVia, "import-via", "registry", "image import method: registry|minikube|kind|k3d|ctr")
	insF.StringVar(&clusterAgentImage, "agent-image", "", "override traffic-agent image (default: same tel2 image)")
	insF.BoolVar(&clusterNoImport, "no-import", false, "skip image import (already present in the cluster)")

	preF := clusterPreflightCmd.Flags()
	preF.StringVar(&clusterImportVia, "import-via", "registry", "intended image import method")

	clusterCmd.AddCommand(clusterPreflightCmd, clusterInstallCmd)
	rootCmd.AddCommand(clusterCmd)
}
