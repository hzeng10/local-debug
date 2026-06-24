package cmd

import (
	"context"

	"github.com/hzeng10/local-debug/internal/tp"
	"github.com/spf13/cobra"
)

var (
	interceptPort    string
	interceptEnvFile string
	interceptMount   string
)

var interceptCmd = &cobra.Command{
	Use:   "intercept <service>",
	Short: "Global (TCP) intercept of a service = full takeover to your laptop",
	Long: `intercept creates a global intercept: all cluster traffic to the target Service is
routed to your local port. This is a full takeover (no header routing, no waypoint,
no license needed) and disrupts other users of the shared service while active.

Prereq: 'telepresence connect' must already be active (it needs sudo/admin once).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		tpc := newTPClient()
		if !tpc.Available() {
			return out.Failf("intercept", "install telepresence or pass --telepresence-bin", errTelepresenceMissing)
		}
		if err := tpc.Intercept(ctx, tp.InterceptOpts{
			Name:      args[0],
			Namespace: flagNamespace,
			Port:      interceptPort,
			EnvFile:   interceptEnvFile,
			Mount:     interceptMount,
		}); err != nil {
			return out.Failf("intercept", "is 'telepresence connect' active? is the namespace correct?", err)
		}
		out.Result("intercept", "Global intercept active for "+args[0]+" — cluster traffic now routes to your laptop.",
			map[string]string{"service": args[0], "namespace": flagNamespace, "port": interceptPort})
		return nil
	},
}

func init() {
	f := interceptCmd.Flags()
	f.StringVar(&interceptPort, "port", "", "local:identifier port mapping (identifier = service port name/number)")
	f.StringVar(&interceptEnvFile, "env-file", "", "write the intercepted pod's env to this file")
	f.StringVar(&interceptMount, "mount", "false", "mount remote volumes: true|false|<path>")
	rootCmd.AddCommand(interceptCmd)
}
