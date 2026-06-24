package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	downKeep   bool
	downStayUp bool
)

type downResult struct {
	LeftIntercepts    []string `json:"leftIntercepts"`
	UninstalledAgents []string `json:"uninstalledAgents,omitempty"`
	RevertedAmbient   []string `json:"revertedAmbient,omitempty"`
	KeptOptOut        []string `json:"keptAmbientOptOut,omitempty"`
	Disconnected      bool     `json:"disconnected"`
	RemovedFiles      []string `json:"removedFiles,omitempty"`
}

var downCmd = &cobra.Command{
	Use:   "down",
	Short: "Leave intercepts, disconnect, and clean up generated files",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		tpc := newTPClient()
		res := downResult{}

		if tpc.Available() {
			if st, err := tpc.Status(ctx); err == nil {
				for _, ic := range st.Intercepts {
					if lerr := tpc.Leave(ctx, ic.Name); lerr == nil {
						res.LeftIntercepts = append(res.LeftIntercepts, ic.Name)
					}
					// Order matters: remove the agent BEFORE reverting the ambient
					// opt-out. An ambient workload that still has the traffic-agent
					// gets its port black-holed, so we only return it to the mesh once
					// the agent is gone. If uninstall fails, keep the opt-out so the
					// workload stays functional (agent present, but out of ambient).
					agentGone := tpc.Uninstall(ctx, ic.Name) == nil
					if agentGone {
						res.UninstalledAgents = append(res.UninstalledAgents, ic.Name)
						if name := revertAmbientOptOut(ctx, ic.Name, ic.Namespace); name != "" {
							res.RevertedAmbient = append(res.RevertedAmbient, name)
						}
					} else if stillOptedOut(ctx, ic.Name, ic.Namespace) {
						res.KeptOptOut = append(res.KeptOptOut, ic.Name)
					}
				}
			}
			if !downStayUp {
				if qerr := tpc.Quit(ctx, true); qerr == nil {
					res.Disconnected = true
				}
			}
		}

		if !downKeep {
			res.RemovedFiles = cleanupGeneratedDir()
		}

		out.Result("down", renderDown(res), res)
		return nil
	},
}

// revertAmbientOptOut clears the dataplane-mode=none that `up` applied for an
// intercepted workload (best-effort; only reverts ldbg's own opt-out). Returns
// "Kind/name" if it reverted, else "".
func revertAmbientOptOut(ctx context.Context, name, namespace string) string {
	cl, err := newK8sClient()
	if err != nil {
		return ""
	}
	ns := namespace
	if ns == "" {
		ns = flagNamespace
	}
	if ns == "" {
		ns = cl.DefaultNamespace()
	}
	wl, err := cl.ResolveWorkload(ctx, ns, name)
	if err != nil || !wl.LdbgAppliedOptOut() {
		return ""
	}
	if cl.ClearAmbientOptOut(ctx, wl) != nil {
		return ""
	}
	return wl.Kind + "/" + wl.Name
}

func renderDown(r downResult) string {
	s := "Torn down."
	if len(r.LeftIntercepts) > 0 {
		s += fmt.Sprintf(" Left intercepts %v.", r.LeftIntercepts)
	}
	if len(r.RevertedAmbient) > 0 {
		s += fmt.Sprintf(" Reverted ambient on %v (clean baseline).", r.RevertedAmbient)
	}
	if len(r.KeptOptOut) > 0 {
		s += fmt.Sprintf("\n! Could not remove the traffic-agent on %v, so they were KEPT out of ambient to stay functional."+
			"\n  Reconnect scoped to the namespace and run 'telepresence uninstall <svc>' to fully restore.", r.KeptOptOut)
	}
	return s
}

// stillOptedOut reports whether the workload still carries ldbg's ambient opt-out
// (used to warn when we could not remove the agent, so we left it out of ambient).
func stillOptedOut(ctx context.Context, name, namespace string) bool {
	cl, err := newK8sClient()
	if err != nil {
		return false
	}
	ns := namespace
	if ns == "" {
		ns = flagNamespace
	}
	if ns == "" {
		ns = cl.DefaultNamespace()
	}
	wl, err := cl.ResolveWorkload(ctx, ns, name)
	return err == nil && wl.LdbgAppliedOptOut()
}

// cleanupGeneratedDir removes the git-ignored .ldbg/ working dir.
func cleanupGeneratedDir() []string {
	const dir = ".ldbg"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var removed []string
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if os.Remove(p) == nil {
			removed = append(removed, p)
		}
	}
	_ = os.Remove(dir)
	return removed
}

func init() {
	f := downCmd.Flags()
	f.BoolVar(&downKeep, "keep-files", false, "keep generated env-files / run configs")
	f.BoolVar(&downStayUp, "stay-connected", false, "leave intercepts but keep the telepresence connection")
	rootCmd.AddCommand(downCmd)
}
