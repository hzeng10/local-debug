package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/tp"
)

// newTPClient resolves the telepresence binary: --telepresence-bin, then PATH, then
// the common per-user install dir (~/.local/bin), so ldbg works even when the binary
// isn't on the agent's PATH.
func newTPClient() *tp.Client {
	bin := flagTPBin
	if bin == "" {
		if _, err := exec.LookPath("telepresence"); err == nil {
			bin = "telepresence"
		} else if home, herr := os.UserHomeDir(); herr == nil {
			cand := filepath.Join(home, ".local", "bin", "telepresence")
			if _, serr := os.Stat(cand); serr == nil {
				bin = cand
			}
		}
		if bin == "" {
			bin = "telepresence"
		}
	}
	return tp.New(bin)
}

// managerNamespace is where the traffic-manager lives (telepresence default).
const managerNamespace = "ambassador"

// newK8sClient builds a client-go client from the global --kubeconfig/--context flags.
func newK8sClient() (*k8s.Client, error) {
	return k8s.NewClient(flagKubeconfig, flagContext)
}

// resolveNamespace returns the namespace to use: --namespace if set, else the
// active kube-context's default namespace.
func resolveNamespace(c *k8s.Client) (string, error) {
	if flagNamespace != "" {
		return flagNamespace, nil
	}
	if ns := c.DefaultNamespace(); ns != "" {
		return ns, nil
	}
	return "", fmt.Errorf("no namespace: pass --namespace/-n or set one in your kube-context")
}
