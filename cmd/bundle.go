package cmd

import (
	"context"

	"github.com/hzeng10/local-debug/internal/offline"
	"github.com/spf13/cobra"
)

var (
	bundleOut     string
	bundleVersion string
	bundleNoPull  bool
)

type bundleResult struct {
	Image   string `json:"image"`
	Tarball string `json:"tarball"`
	Pulled  bool   `json:"pulled"`
}

var bundleCmd = &cobra.Command{
	Use:   "bundle",
	Short: "On an internet machine, save the traffic-manager image as a transfer bundle",
	Long: `bundle resolves the traffic-manager/agent image for the targeted Telepresence
version (ghcr.io/telepresenceio/tel2:<ver> — one image serves both manager and agent),
docker-pulls it, and docker-saves it to a tarball you carry into the air-gapped
environment, where 'ldbg cluster install' imports it and installs the traffic-manager.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := context.Background()
		image := offline.ImageFor(bundleVersion)
		res := bundleResult{Image: image, Tarball: bundleOut}

		if !bundleNoPull {
			out.Info("… docker pull %s", image)
			if err := offline.DockerPull(ctx, image); err != nil {
				return out.Failf("bundle", "run on a machine with internet + docker", err)
			}
			res.Pulled = true
		}
		out.Info("… docker save → %s", bundleOut)
		if err := offline.DockerSave(ctx, image, bundleOut); err != nil {
			return out.Failf("bundle", "", err)
		}
		out.Result("bundle", "Bundled "+image+" → "+bundleOut+
			"\nCarry it to the air-gapped env, then: ldbg cluster install --bundle "+bundleOut, res)
		return nil
	},
}

func init() {
	f := bundleCmd.Flags()
	f.StringVar(&bundleOut, "out", "tel2-bundle.tar", "output tarball path")
	f.StringVar(&bundleVersion, "tp-version", TelepresenceVersion, "Telepresence version to bundle")
	f.BoolVar(&bundleNoPull, "no-pull", false, "skip docker pull (image already present locally)")
	rootCmd.AddCommand(bundleCmd)
}
