// Command ldbg (local-debug) runs a Spring Boot microservice on a laptop while it
// behaves as a live instance of a service in a remote Istio-ambient Kubernetes cluster.
//
// It is a thin wrapper around Telepresence that adds Spring-Boot- and ClaudeCode-specific
// glue: cluster-env sync, ambient-mode preflight, and machine-readable (--json) output so
// an AI agent can drive the debug loop alongside a developer.
package main

import "github.com/hzeng10/local-debug/cmd"

func main() {
	cmd.Execute()
}
