package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
)

// statusReport is the shape ldbg status --json returns: everything an agent needs
// to decide what to do next without parsing human text.
type statusReport struct {
	TelepresenceFound bool     `json:"telepresenceFound"`
	Connected         bool     `json:"connected"`
	KubeContext       string   `json:"kubeContext,omitempty"`
	Namespace         string   `json:"namespace,omitempty"`
	InterceptActive   bool     `json:"interceptActive"`
	Intercepts        []string `json:"intercepts,omitempty"`
	ManagerInstalled  bool     `json:"managerInstalled"`
	ClusterReachable  bool     `json:"clusterReachable"`
	ClusterVersion    string   `json:"clusterVersion,omitempty"`
	Hint              string   `json:"hint,omitempty"`
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report connection / intercept state (use --json for ClaudeCode)",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		rep := statusReport{}

		tpc := newTPClient()
		rep.TelepresenceFound = tpc.Available()
		if rep.TelepresenceFound {
			if st, err := tpc.Status(ctx); err == nil {
				rep.Connected = st.Connected
				rep.KubeContext = st.KubernetesContext
				rep.Namespace = st.Namespace
				rep.ManagerInstalled = st.ManagerInstalled
				rep.InterceptActive = len(st.Intercepts) > 0
				for _, i := range st.Intercepts {
					rep.Intercepts = append(rep.Intercepts, i.Name)
				}
			}
		}

		// Cluster reachability is independent of the telepresence daemon.
		if cl, err := newK8sClient(); err == nil {
			if v, perr := cl.Ping(ctx); perr == nil {
				rep.ClusterReachable = true
				rep.ClusterVersion = v
			}
		}

		rep.Hint = nextStepHint(rep)
		out.Result("status", renderStatus(rep), rep)
		return nil
	},
}

func nextStepHint(r statusReport) string {
	switch {
	case !r.TelepresenceFound:
		return "telepresence client not found; install it or pass --telepresence-bin"
	case !r.ClusterReachable:
		return "cluster unreachable; check kube-context/kubeconfig"
	case !r.Connected:
		return "not connected; run 'telepresence connect' (needs sudo/admin once), then 'ldbg up <service>'"
	case !r.InterceptActive:
		return "connected; run 'ldbg up <service>' to start a global intercept"
	default:
		return "intercept active; run your app locally and use 'ldbg test'"
	}
}

func renderStatus(r statusReport) string {
	s := fmt.Sprintf("telepresence: found=%v connected=%v\n", r.TelepresenceFound, r.Connected)
	s += fmt.Sprintf("cluster:      reachable=%v version=%s context=%s\n", r.ClusterReachable, r.ClusterVersion, r.KubeContext)
	s += fmt.Sprintf("intercepts:   active=%v %v\n", r.InterceptActive, r.Intercepts)
	s += "hint:         " + r.Hint
	return s
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
