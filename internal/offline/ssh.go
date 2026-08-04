package offline

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
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

	// Transport picks how to talk SSH: "system" shells out to ssh/scp (honours
	// the user's ssh config, jump hosts, agent), "native" speaks SSH from inside
	// ldbg so a password can come from LDBG_SSH_PASSWORD without a TTY.
	Transport      string // auto | system | native
	Port           int    // native transport only (system uses --ssh-opts -p)
	KeyFile        string // native transport only
	StrictHostKey  bool   // native transport only
	ConnectTimeout time.Duration
	// Interactive keeps the system transport able to prompt. Off by default when
	// stdin is not a terminal, which is exactly how an agent invokes ldbg — there
	// a prompt can never be answered, so failing fast beats hanging.
	Interactive bool
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
	Transport   string   `json:"transport,omitempty"`
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

// nodeConn is one node's command channel. The native implementation authenticates
// once and reuses the connection; the system one spawns ssh per call.
type nodeConn interface {
	run(ctx context.Context, cmd string) (string, error)
	upload(ctx context.Context, localPath, remotePath string) error
	close()
}

// UseNative reports which transport an SSHOpts resolves to. Setting the password
// environment variable is the signal for "auto": it is the only case the system
// ssh binary cannot serve.
func (o SSHOpts) UseNative() bool {
	switch strings.ToLower(o.Transport) {
	case "native":
		return true
	case "system":
		return false
	default:
		return os.Getenv(PasswordEnv) != ""
	}
}

// TransferAndImport copies the archive to one node and loads it into that node's
// runtime. It never returns an error; the outcome (including failure) is in
// NodeResult so one unreachable node does not abort the rest of the cluster.
//
// Everything after the transfer runs as a SINGLE remote command — import, verify
// and clean-up — so a node costs two round trips instead of four (and, with the
// system transport and password auth, two prompts instead of four).
func TransferAndImport(ctx context.Context, t Target, tarPath, image string, o SSHOpts) NodeResult {
	native := o.UseNative()
	res := NodeResult{Node: t.Name, Address: t.Address, Runtime: string(t.Runtime),
		Transport: map[bool]string{true: "native", false: "system"}[native]}
	target := sshTarget(t.Address, o.User)
	remote := remotePath(o.RemoteTmp, tarPath)

	verify, verr := VerifyCmd(t.Runtime, image, o.Sudo)
	imp, ierr := ImportCmd(t.Runtime, remote, o.Sudo, o.ImportCmd)
	if ierr != nil {
		res.Error = ierr.Error()
		return res
	}
	work := remoteScript(imp, verify, remote, verr == nil, o.KeepRemote)

	// Dry run: show the exact commands and touch nothing.
	if o.DryRun {
		res.Commands = plannedCommands(o, native, tarPath, target, remote, work)
		return res
	}

	conn, err := dial(ctx, t.Address, o, native)
	if err != nil {
		res.Error = fmt.Sprintf("connect: %v", err)
		return res
	}
	defer conn.close()

	// Already there? Then this node is done — makes re-runs cheap and idempotent.
	if o.SkipPresent && verr == nil {
		if _, err := conn.run(ctx, verify); err == nil {
			res.Skipped, res.Verified = true, true
			return res
		}
	}

	if err := conn.upload(ctx, tarPath, remote); err != nil {
		res.Error = fmt.Sprintf("transfer: %v", err)
		return res
	}
	res.Transferred = true

	out, runErr := conn.run(ctx, work)
	imported, verified, parsed := parseStatus(out)
	switch {
	case parsed && !imported:
		res.Error = fmt.Sprintf("import failed on the node%s", tail(out))
	case parsed && verr == nil && !verified:
		res.Error = "the load command succeeded but the image is not visible to the runtime — check the runtime and its namespace"
		res.Imported = true
	case parsed:
		res.Imported, res.Verified = true, verr == nil
	case runErr != nil:
		res.Error = fmt.Sprintf("import: %v%s", runErr, tail(out))
	default:
		res.Imported = true // ran clean but printed no marker; treat as imported
	}
	return res
}

// statusMarker lets one round trip report both stages separately.
var statusRe = regexp.MustCompile(`ldbg-status i=(-?\d+) v=(-?\d+)`)

// remoteScript is import → verify → clean-up in one shell command, reporting each
// stage's exit code so a single connection still yields a precise diagnosis.
func remoteScript(imp, verify, remote string, canVerify, keepRemote bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s; i=$?; v=0; ", imp)
	if canVerify {
		fmt.Fprintf(&b, "if [ $i -eq 0 ]; then %s; v=$?; fi; ", verify)
	}
	if !keepRemote {
		fmt.Fprintf(&b, "rm -f %q; ", remote)
	}
	b.WriteString(`echo "ldbg-status i=$i v=$v"; if [ $i -ne 0 ]; then exit $i; fi; exit $v`)
	return b.String()
}

func parseStatus(out string) (imported, verified, parsed bool) {
	m := statusRe.FindStringSubmatch(out)
	if m == nil {
		return false, false, false
	}
	i, _ := strconv.Atoi(m[1])
	v, _ := strconv.Atoi(m[2])
	return i == 0, v == 0, true
}

func dial(ctx context.Context, address string, o SSHOpts, native bool) (nodeConn, error) {
	if native {
		return dialNative(ctx, address, o)
	}
	return &sysConn{target: sshTarget(address, o.User), opts: o}, nil
}

// plannedCommands is what --dry-run prints.
func plannedCommands(o SSHOpts, native bool, tarPath, target, remote, work string) []string {
	if native {
		return []string{
			fmt.Sprintf("[native ssh] %s: upload %s → %s", target, tarPath, remote),
			fmt.Sprintf("[native ssh] %s: %s", target, work),
		}
	}
	c := &sysConn{target: target, opts: o}
	var cmds []string
	if up, ok := c.scpArgs(tarPath, remote); ok {
		cmds = append(cmds, strings.Join(up, " "))
	} else {
		cmds = append(cmds, fmt.Sprintf("ssh %s 'cat > %s' < %s", target, remote, tarPath))
	}
	return append(cmds, strings.Join(c.sshArgs(work), " "))
}

// ---- system transport (shells out to ssh/scp) ----

type sysConn struct {
	target string
	opts   SSHOpts
}

func (c *sysConn) run(ctx context.Context, cmd string) (string, error) {
	return runCapture(ctx, c.sshArgs(cmd))
}

func (c *sysConn) upload(ctx context.Context, localPath, remotePath string) error {
	if args, ok := c.scpArgs(localPath, remotePath); ok {
		out, err := runCapture(ctx, args)
		if err != nil {
			// scp's own message ("Connection timed out", "Permission denied") is
			// the actionable part; "exit status 255" on its own is not.
			return fmt.Errorf("%v%s", err, tail(out))
		}
		return nil
	}
	// No scp on this machine: pipe the archive over the same ssh authentication.
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	args := c.sshArgs(fmt.Sprintf("cat > %q", remotePath))
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdin = f
	var se bytes.Buffer
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v%s", err, tail(se.String()))
	}
	return nil
}

func (c *sysConn) close() {}

func (c *sysConn) sshArgs(remoteCmd string) []string {
	args := append([]string{"ssh"}, c.batchOpts()...)
	args = append(args, c.opts.Opts...)
	return append(args, c.target, remoteCmd)
}

func (c *sysConn) scpArgs(localPath, remote string) ([]string, bool) {
	if _, err := exec.LookPath("scp"); err != nil {
		return nil, false
	}
	args := append([]string{"scp"}, c.batchOpts()...)
	args = append(args, c.opts.Opts...)
	return append(args, localPath, c.target+":"+remote), true
}

// batchOpts keeps a non-interactive run deterministic: without a terminal ssh
// cannot ask anything, so BatchMode turns "hang forever" into an immediate,
// actionable error, and ConnectTimeout bounds an unreachable node (the default
// is minutes). --ssh-opts still wins, since it is appended after these.
func (c *sysConn) batchOpts() []string {
	if c.opts.Interactive {
		return nil
	}
	secs := int(c.opts.ConnectTimeout.Seconds())
	if secs <= 0 {
		secs = 10
	}
	return []string{"-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", secs)}
}

// ---- shared helpers ----

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

// runCapture runs a command capturing output; stdin stays connected so an
// interactive run can still answer a prompt.
func runCapture(ctx context.Context, args []string) (string, error) {
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
