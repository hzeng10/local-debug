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

// AutoScript is the fallback for a runtime the kubelet reports in a form ldbg
// does not recognize — customized PaaS distributions do exactly that. The node
// is reachable over SSH anyway, so instead of giving up, probe which engine is
// actually installed and import + verify with that one, still in a single
// connection. The winning engine travels back as rt=<name> in the status
// marker, and s=1 marks a skip-present hit. docker is probed first: an
// unrecognized report almost always comes from a renamed docker fork, while
// standalone containerd and cri-o report schemes the normal path already
// understands.
func AutoScript(remoteTar, image string, sudo, skipPresent, keepRemote bool) string {
	pre := sudoPrefix(sudo)
	if pre != "" {
		pre += " "
	}
	branches := []struct{ cli, rt, load, verify string }{
		{"docker", "docker",
			fmt.Sprintf("%sdocker load -i %q", pre, remoteTar),
			fmt.Sprintf("%sdocker image inspect %q >/dev/null 2>&1", pre, image)},
		{"ctr", "containerd",
			fmt.Sprintf("%sctr -n %s images import %q", pre, containerdNamespace, remoteTar),
			fmt.Sprintf("%sctr -n %s images ls -q | grep -qF %q", pre, containerdNamespace, image)},
		{"k3s", "containerd",
			fmt.Sprintf("%sk3s ctr -n %s images import %q", pre, containerdNamespace, remoteTar),
			fmt.Sprintf("%sk3s ctr -n %s images ls -q | grep -qF %q", pre, containerdNamespace, image)},
		{"nerdctl", "containerd",
			fmt.Sprintf("%snerdctl -n %s load -i %q", pre, containerdNamespace, remoteTar),
			fmt.Sprintf("%snerdctl -n %s image inspect %q >/dev/null 2>&1", pre, containerdNamespace, image)},
		{"podman", "cri-o",
			fmt.Sprintf("%spodman load -i %q", pre, remoteTar),
			fmt.Sprintf("%spodman image exists %q", pre, image)},
		{"isula", "isulad",
			fmt.Sprintf("%sisula load -i %q", pre, remoteTar),
			fmt.Sprintf("%sisula inspect %q >/dev/null 2>&1", pre, image)},
	}
	var b strings.Builder
	b.WriteString("rt=none; i=127; v=1; s=0; ")
	for n, br := range branches {
		kw := "elif"
		if n == 0 {
			kw = "if"
		}
		fmt.Fprintf(&b, "%s command -v %s >/dev/null 2>&1; then rt=%s; ", kw, br.cli, br.rt)
		if skipPresent {
			fmt.Fprintf(&b, "if %s; then s=1; i=0; v=0; else ", br.verify)
		}
		fmt.Fprintf(&b, "%s; i=$?; if [ $i -eq 0 ]; then %s; v=$?; fi", br.load, br.verify)
		if skipPresent {
			b.WriteString("; fi")
		}
		b.WriteString("; ")
	}
	b.WriteString("fi; ")
	if !keepRemote {
		fmt.Fprintf(&b, "rm -f %q; ", remoteTar)
	}
	b.WriteString(`echo "ldbg-status i=$i v=$v rt=$rt s=$s"; if [ $i -ne 0 ]; then exit $i; fi; exit $v`)
	return b.String()
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
