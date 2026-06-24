// Package cmd defines the ldbg CLI surface (cobra commands). Command bodies stay
// thin: they parse flags, then delegate to the internal/* packages and render via
// internal/output. Every command supports --json so ClaudeCode can drive ldbg.
package cmd

import (
	"fmt"
	"os"

	"github.com/hzeng10/local-debug/internal/output"
	"github.com/spf13/cobra"
)

// Global flags, resolved once in PersistentPreRun and shared with subcommands.
var (
	flagJSON       bool
	flagContext    string
	flagNamespace  string
	flagKubeconfig string
	flagTPBin      string

	// out is the shared Printer, constructed from the global flags.
	out *output.Printer
)

var rootCmd = &cobra.Command{
	Use:   "ldbg",
	Short: "Debug a Spring Boot service locally as a live instance of a remote Istio-ambient cluster",
	Long: `ldbg (local-debug) lets you run a Spring Boot microservice on your laptop while it
behaves as the live instance of a service in a remote, shared Kubernetes cluster running
Istio in ambient mode: it receives that service's real traffic and calls the real in-cluster
dependencies (DB, MQ, Redis, peer services), debuggable in your IDE and drivable by ClaudeCode.

It is a thin wrapper around Telepresence (free global/TCP intercept = full takeover) with
Spring Boot config sync, ambient-mode preflight, and --json output for AI agents.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		out = output.New(os.Stdout, os.Stderr, flagJSON)
	},
}

// Execute is the CLI entrypoint.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		// Body already rendered the error (human or JSON); just set exit code.
		os.Exit(1)
	}
}

func init() {
	pf := rootCmd.PersistentFlags()
	pf.BoolVar(&flagJSON, "json", false, "machine-readable JSON output (for ClaudeCode/scripts)")
	pf.StringVar(&flagContext, "context", "", "kube-context to use (default: current context)")
	pf.StringVarP(&flagNamespace, "namespace", "n", "", "Kubernetes namespace of the target service")
	pf.StringVar(&flagKubeconfig, "kubeconfig", "", "path to kubeconfig (default: $KUBECONFIG or ~/.kube/config)")
	pf.StringVar(&flagTPBin, "telepresence-bin", "", "path to the telepresence binary (default: PATH, then ~/.local/bin)")
}

// notImplemented is a placeholder for commands whose logic lands in a later build
// phase. It still renders through the shared Printer so --json stays well-formed.
func notImplemented(cmd *cobra.Command, phase string) error {
	return out.Failf(cmd.Name(), "implemented in build "+phase,
		fmt.Errorf("%q is not implemented yet (%s)", cmd.CommandPath(), phase))
}
