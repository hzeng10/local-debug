package cmd

import "github.com/spf13/cobra"

// Version is the ldbg build version, overridable at link time via
// -ldflags "-X github.com/hzeng10/local-debug/cmd.Version=...".
var Version = "0.0.0-dev"

// TelepresenceVersion is the Telepresence client/traffic-manager version this
// build of ldbg targets. The traffic-agent and traffic-manager images share the
// same tag (ghcr.io/telepresenceio/tel2:<this>).
var TelepresenceVersion = "2.29.0"

type versionInfo struct {
	Ldbg             string `json:"ldbg"`
	Telepresence     string `json:"telepresence"`
	TrafficManagerNS string `json:"trafficManagerNamespace"`
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print ldbg and target Telepresence versions",
	RunE: func(cmd *cobra.Command, args []string) error {
		info := versionInfo{Ldbg: Version, Telepresence: TelepresenceVersion, TrafficManagerNS: "ambassador"}
		out.Result("version",
			"ldbg "+info.Ldbg+" (targets Telepresence "+info.Telepresence+")", info)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
