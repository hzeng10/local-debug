package offline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SSHOpts tunes how ldbg reaches the cluster nodes.
type SSHOpts struct {
	User        string   // default login when a node address carries none
	Opts        []string // extra ssh/scp options, e.g. -i key -p 2222 -o StrictHostKeyChecking=no
	RemoteTmp   string   // where the archive lands on the node (default /tmp)
	Sudo        bool     // prefix the runtime commands with sudo
	ImportCmd   string   // override the whole load command (%s = tar path)
	SkipPresent bool     // leave nodes that already have the image alone
	KeepRemote  bool     // do not delete the transferred archive
	DryRun      bool     // print what would run, execute nothing
}

// Target is one node to import into.
type Target struct {
	Name    string // node name, for reporting
	Address string // host or user@host
	Runtime Runtime
}

// NodeResult is the per-node outcome, shaped for both the human table and --json
// so an agent can tell exactly which nodes are ready.
type NodeResult struct {
	Node        string   `json:"node"`
	Address     string   `json:"address"`
	Runtime     string   `json:"runtime"`
	Transferred bool     `json:"transferred"`
	Imported    bool     `json:"imported"`
	Verified    bool     `json:"verified"`
	Skipped     bool     `json:"skipped"`
	Commands    []string `json:"commands,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// OK reports whether the node ended up holding the image. An import that could
// not be verified still counts: verification is skipped only for a runtime ldbg
// does not know (where the user supplied --import-cmd), and a failed
// verification sets Error instead.
func (r NodeResult) OK() bool { return r.Error == "" && (r.Verified || r.Skipped || r.Imported) }

// TransferAndImport copies the archive to one node and loads it into that node's
// runtime: verify-if-asked → transfer → import → verify → remove the archive.
// It never returns an error; the outcome (including failure) is in NodeResult so
// one unreachable node does not abort the rest of the cluster.
func TransferAndImport(ctx context.Context, t Target, tarPath, image string, o SSHOpts) NodeResult {
	res := NodeResult{Node: t.Name, Address: t.Address, Runtime: string(t.Runtime)}
	target := sshTarget(t.Address, o.User)
	remote := remotePath(o.RemoteTmp, tarPath)

	verify, verr := VerifyCmd(t.Runtime, image, o.Sudo)
	imp, ierr := ImportCmd(t.Runtime, remote, o.Sudo, o.ImportCmd)
	if ierr != nil {
		res.Error = ierr.Error()
		return res
	}

	// Already there? Then this node is done — makes re-runs cheap and idempotent.
	if o.SkipPresent && verr == nil {
		cmd := sshArgs(o, target, verify)
		res.Commands = append(res.Commands, "ssh "+strings.Join(cmd[1:], " "))
		if !o.DryRun {
			if _, err := runQuiet(ctx, cmd); err == nil {
				res.Skipped, res.Verified = true, true
				return res
			}
		}
	}

	// 1) transfer
	scpCmd, useSCP := scpArgs(o, tarPath, target, remote)
	if useSCP {
		res.Commands = append(res.Commands, strings.Join(scpCmd, " "))
	} else {
		res.Commands = append(res.Commands, fmt.Sprintf("ssh %s 'cat > %s' < %s", target, remote, tarPath))
	}
	// 2) import, 3) verify, 4) clean up
	res.Commands = append(res.Commands, fmt.Sprintf("ssh %s %q", target, imp))
	if verr == nil {
		res.Commands = append(res.Commands, fmt.Sprintf("ssh %s %q", target, verify))
	}
	if !o.KeepRemote {
		res.Commands = append(res.Commands, fmt.Sprintf("ssh %s %q", target, "rm -f "+remote))
	}
	if o.DryRun {
		return res
	}

	if err := transfer(ctx, o, tarPath, target, remote, scpCmd, useSCP); err != nil {
		res.Error = fmt.Sprintf("transfer: %v", err)
		return res
	}
	res.Transferred = true

	if out, err := runQuiet(ctx, sshArgs(o, target, imp)); err != nil {
		res.Error = fmt.Sprintf("import: %v%s", err, tail(out))
		cleanup(ctx, o, target, remote)
		return res
	}
	res.Imported = true

	if verr == nil {
		if _, err := runQuiet(ctx, sshArgs(o, target, verify)); err != nil {
			res.Error = "the load command succeeded but the image is not visible to the runtime — check the runtime/namespace"
			cleanup(ctx, o, target, remote)
			return res
		}
		res.Verified = true
	}
	cleanup(ctx, o, target, remote)
	return res
}

func transfer(ctx context.Context, o SSHOpts, tarPath, target, remote string, scpCmd []string, useSCP bool) error {
	if useSCP {
		_, err := runQuiet(ctx, scpCmd)
		return err
	}
	// No scp on this machine: pipe the archive over the same ssh authentication.
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	args := sshArgs(o, target, fmt.Sprintf("cat > %q", remote))
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	c.Stdin = f
	var se bytes.Buffer
	c.Stderr = &se
	if err := c.Run(); err != nil {
		return fmt.Errorf("%v%s", err, tail(se.String()))
	}
	return nil
}

func cleanup(ctx context.Context, o SSHOpts, target, remote string) {
	if o.KeepRemote {
		return
	}
	_, _ = runQuiet(ctx, sshArgs(o, target, "rm -f "+shellQuote(remote)))
}

// sshArgs builds the ssh invocation for a remote shell command.
func sshArgs(o SSHOpts, target, remoteCmd string) []string {
	args := []string{"ssh"}
	args = append(args, o.Opts...)
	return append(args, target, remoteCmd)
}

// scpArgs builds the scp invocation, reporting false when scp is unavailable so
// the caller falls back to piping through ssh.
func scpArgs(o SSHOpts, localPath, target, remote string) ([]string, bool) {
	if _, err := exec.LookPath("scp"); err != nil {
		return nil, false
	}
	args := []string{"scp"}
	args = append(args, o.Opts...)
	return append(args, localPath, target+":"+remote), true
}

// sshTarget applies the default user when the address does not carry one.
func sshTarget(addr, user string) string {
	if strings.Contains(addr, "@") || user == "" {
		return addr
	}
	return user + "@" + addr
}

// remotePath is where the archive lands on the node.
func remotePath(tmp, localPath string) string {
	if tmp == "" {
		tmp = "/tmp"
	}
	return strings.TrimRight(tmp, "/") + "/" + filepath.Base(localPath)
}

// runQuiet runs a command capturing output; stdin stays connected so ssh can
// still prompt for a password or a host-key confirmation.
func runQuiet(ctx context.Context, args []string) (string, error) {
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	var buf bytes.Buffer
	c.Stdout, c.Stderr = &buf, &buf
	c.Stdin = os.Stdin
	err := c.Run()
	return buf.String(), err
}

// tail returns the last line of command output, to append to an error without
// dumping an entire transfer log.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	return ": " + s
}

func shellQuote(s string) string { return fmt.Sprintf("%q", s) }
