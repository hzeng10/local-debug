package offline

import (
	"fmt"
	"strings"
)

// Runtime is a node's container runtime. Each one loads an image archive with a
// different command, and picking the wrong one simply fails — so ldbg reads the
// answer from the cluster (node.status.nodeInfo.containerRuntimeVersion) instead
// of asking the user.
type Runtime string

const (
	RuntimeContainerd Runtime = "containerd"
	RuntimeDocker     Runtime = "docker"
	RuntimeCRIO       Runtime = "cri-o"
	RuntimeUnknown    Runtime = "unknown"
)

// RuntimeOf normalizes the runtime name the kubelet reports.
func RuntimeOf(name string) Runtime {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "containerd":
		return RuntimeContainerd
	case "docker":
		return RuntimeDocker
	case "cri-o", "crio":
		return RuntimeCRIO
	default:
		return RuntimeUnknown
	}
}

// containerdNamespace is the only namespace the kubelet looks in. Importing into
// containerd's default namespace instead is the classic silent failure: the
// command succeeds, and the cluster still reports ImagePullBackOff.
const containerdNamespace = "k8s.io"

// ImportCmd returns the shell command that loads tarPath into the node's runtime.
// A shell string (rather than argv) is right here: it runs through `ssh`, which
// hands the whole thing to the remote shell anyway, and containerd needs a
// `command -v` probe to find its CLI.
//
// override replaces the whole command; %s in it is substituted with the tar path
// so unusual distributions can be supported without ldbg knowing about them.
func ImportCmd(rt Runtime, tarPath string, sudo bool, override string) (string, error) {
	if override != "" {
		return maybeSudo(substituteTar(override, tarPath), sudo), nil
	}
	switch rt {
	case RuntimeContainerd:
		// k3s and RKE2 embed containerd and ship no standalone `ctr`; nerdctl is
		// the third common spelling. Probe at run time rather than guess.
		return fmt.Sprintf(
			`if command -v ctr >/dev/null 2>&1; then %s ctr -n %s images import %q; `+
				`elif command -v k3s >/dev/null 2>&1; then %s k3s ctr -n %s images import %q; `+
				`elif command -v nerdctl >/dev/null 2>&1; then %s nerdctl -n %s load -i %q; `+
				`else echo "no ctr/k3s/nerdctl on this node — pass --import-cmd" >&2; exit 127; fi`,
			sudoPrefix(sudo), containerdNamespace, tarPath,
			sudoPrefix(sudo), containerdNamespace, tarPath,
			sudoPrefix(sudo), containerdNamespace, tarPath), nil
	case RuntimeDocker:
		return maybeSudo(fmt.Sprintf("docker load -i %q", tarPath), sudo), nil
	case RuntimeCRIO:
		return maybeSudo(fmt.Sprintf("podman load -i %q", tarPath), sudo), nil
	default:
		return "", fmt.Errorf("unknown container runtime — pass --import-cmd with the load command for this node")
	}
}

// VerifyCmd returns a command that exits 0 only when the image is present in the
// runtime the kubelet actually reads from.
func VerifyCmd(rt Runtime, image string, sudo bool) (string, error) {
	switch rt {
	case RuntimeContainerd:
		return fmt.Sprintf(
			`if command -v ctr >/dev/null 2>&1; then %s ctr -n %s images ls -q; `+
				`elif command -v k3s >/dev/null 2>&1; then %s k3s ctr -n %s images ls -q; `+
				`else %s nerdctl -n %s images -q; fi | grep -qF %q`,
			sudoPrefix(sudo), containerdNamespace,
			sudoPrefix(sudo), containerdNamespace,
			sudoPrefix(sudo), containerdNamespace, image), nil
	case RuntimeDocker:
		return maybeSudo(fmt.Sprintf("docker image inspect %q >/dev/null 2>&1", image), sudo), nil
	case RuntimeCRIO:
		return maybeSudo(fmt.Sprintf("crictl images -q %q | grep -q .", image), sudo), nil
	default:
		return "", fmt.Errorf("unknown container runtime — cannot verify the image on this node")
	}
}

func sudoPrefix(sudo bool) string {
	if sudo {
		return "sudo"
	}
	return ""
}

// maybeSudo prefixes a single command with sudo (the containerd form builds its
// own prefixes because it is a multi-branch shell statement).
func maybeSudo(cmd string, sudo bool) string {
	if !sudo {
		return cmd
	}
	return "sudo " + cmd
}

// substituteTar fills %s in an --import-cmd override, appending the path when the
// override has no placeholder.
func substituteTar(override, tarPath string) string {
	if strings.Contains(override, "%s") {
		return strings.ReplaceAll(override, "%s", tarPath)
	}
	return override + " " + tarPath
}
