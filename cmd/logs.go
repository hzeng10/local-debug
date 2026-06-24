package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/spf13/cobra"
)

var (
	logsFollow    bool
	logsTail      int64
	logsManager   bool
	logsContainer string
)

var logsCmd = &cobra.Command{
	Use:   "logs [service]",
	Short: "Tail logs from the intercepted workload's pods (or the traffic-manager)",
	Long: `logs streams the cluster-side logs that matter during an intercept: the target
workload's pods (including the injected traffic-agent container, whose logs show whether
traffic is being routed to your laptop). Use --manager for the traffic-manager.

Your local app's own logs appear in your IDE/terminal (ldbg does not own that process
unless you launched it with 'ldbg up --run').`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		cl, err := newK8sClient()
		if err != nil {
			return out.Failf("logs", "check --kubeconfig/--context", err)
		}

		if logsManager {
			pod, perr := cl.ManagerPod(ctx, managerNamespace)
			if perr != nil {
				return out.Failf("logs", "is the traffic-manager installed?", perr)
			}
			return cl.StreamPodLogs(ctx, managerNamespace, pod, "traffic-manager", "[traffic-manager]", logsFollow, logsTail, os.Stdout)
		}

		if len(args) == 0 {
			return out.Failf("logs", "give a <service> or use --manager", fmt.Errorf("service name required"))
		}
		ns, err := resolveNamespace(cl)
		if err != nil {
			return out.Failf("logs", "", err)
		}
		wl, err := cl.ResolveWorkload(ctx, ns, args[0])
		if err != nil {
			return out.Failf("logs", "is the service/workload name and namespace correct?", err)
		}
		pods, err := cl.PodsForWorkload(ctx, wl)
		if err != nil {
			return out.Failf("logs", "", err)
		}
		if len(pods) == 0 {
			return out.Failf("logs", "is the workload running?", fmt.Errorf("no pods for %q", args[0]))
		}

		// Stream each pod/container — concurrently when following, sequentially otherwise.
		var wg sync.WaitGroup
		for _, p := range pods {
			containers := p.Containers
			if logsContainer != "" {
				containers = []string{logsContainer}
			}
			for _, c := range containers {
				prefix := fmt.Sprintf("[%s/%s]", p.Name, c)
				if logsFollow {
					wg.Add(1)
					go func(pod, cont, pfx string) {
						defer wg.Done()
						if e := cl.StreamPodLogs(ctx, ns, pod, cont, pfx, true, logsTail, os.Stdout); e != nil {
							fmt.Fprintf(os.Stderr, "%s log stream ended: %v\n", pfx, e)
						}
					}(p.Name, c, prefix)
				} else if e := cl.StreamPodLogs(ctx, ns, p.Name, c, prefix, false, logsTail, os.Stdout); e != nil {
					fmt.Fprintf(os.Stderr, "%s %v\n", prefix, e)
				}
			}
		}
		if logsFollow {
			wg.Wait()
		}
		return nil
	},
}

func init() {
	f := logsCmd.Flags()
	f.BoolVarP(&logsFollow, "follow", "f", false, "stream logs")
	f.Int64Var(&logsTail, "tail", 50, "number of recent lines to show (-1 = all)")
	f.BoolVar(&logsManager, "manager", false, "tail the traffic-manager instead of a workload")
	f.StringVar(&logsContainer, "container", "", "only this container (default: all, incl. traffic-agent)")
	rootCmd.AddCommand(logsCmd)
}
