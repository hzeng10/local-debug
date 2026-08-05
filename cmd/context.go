package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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

const (
	// DefaultManagerNamespace is where telepresence puts the traffic-manager
	// unless told otherwise.
	DefaultManagerNamespace = "ambassador"
	// ManagerNamespaceEnv sets it without repeating the flag on every command,
	// which is what an agent or a CI job wants.
	ManagerNamespaceEnv = "LDBG_MANAGER_NAMESPACE"
)

// managerNS is where the traffic-manager lives. It has to agree across install,
// connect and log reading: `telepresence connect --manager-namespace` overrides
// even the client's own config file, so a mismatch here means the daemon looks
// for a manager that is not there. Precedence: --manager-namespace, then the
// environment, then telepresence's own default.
func managerNS() string {
	if ns := strings.TrimSpace(flagManagerNamespace); ns != "" {
		return ns
	}
	return DefaultManagerNamespace
}

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
