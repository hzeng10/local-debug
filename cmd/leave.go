package cmd

import (
	"context"
	"errors"

	"github.com/spf13/cobra"
)

var errTelepresenceMissing = errors.New("telepresence client not found")

var leaveCmd = &cobra.Command{
	Use:   "leave <service>",
	Short: "Stop a global intercept (cluster traffic returns to the in-cluster pod)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		tpc := newTPClient()
		if !tpc.Available() {
			return out.Failf("leave", "install telepresence or pass --telepresence-bin", errTelepresenceMissing)
		}
		if err := tpc.Leave(ctx, args[0]); err != nil {
			return out.Failf("leave", "", err)
		}
		out.Result("leave", "Left intercept "+args[0]+"; cluster traffic returns to the in-cluster pod.",
			map[string]string{"service": args[0]})
		return nil
	},
}

func init() {
	rootCmd.AddCommand(leaveCmd)
}
