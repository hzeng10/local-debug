package offline

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

// PasswordEnv is where the native transport reads the node password from. It is
// deliberately an environment variable and not a flag: a password in argv shows
// up in `ps`, shell history and CI logs.
const PasswordEnv = "LDBG_SSH_PASSWORD"

// nativeConn speaks SSH from inside ldbg instead of shelling out. It exists for
// one reason: an AI agent has no TTY, and OpenSSH only ever reads a password
// from /dev/tty — so password-authenticated nodes are simply unreachable through
// the system ssh binary. One connection serves the whole node (authenticate
// once), which also removes the repeated prompts a human would otherwise face.
type nativeConn struct {
	c    *ssh.Client
	addr string
}

func dialNative(ctx context.Context, address string, o SSHOpts) (*nativeConn, error) {
	user, host := splitUserHost(address, o.User)
	if user == "" {
		return nil, fmt.Errorf("no SSH user for %q — pass --ssh-user or use user@host in --nodes", address)
	}
	hostPort := withPort(host, o.Port)

	auths, err := nativeAuths(o)
	if err != nil {
		return nil, err
	}
	hk, err := hostKeyCallback(o.StrictHostKey)
	if err != nil {
		return nil, err
	}

	timeout := o.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, err
	}
	cc, chans, reqs, err := ssh.NewClientConn(conn, hostPort, &ssh.ClientConfig{
		User:            user,
		Auth:            auths,
		HostKeyCallback: hk,
		Timeout:         timeout,
	})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &nativeConn{c: ssh.NewClient(cc, chans, reqs), addr: hostPort}, nil
}

// nativeAuths builds the authentication chain: the password from the environment
// first (that is the point of this transport), then an explicit key, then the
// agent.
func nativeAuths(o SSHOpts) ([]ssh.AuthMethod, error) {
	var auths []ssh.AuthMethod
	if pw := os.Getenv(PasswordEnv); pw != "" {
		auths = append(auths, ssh.Password(pw))
	}
	if o.KeyFile != "" {
		signer, err := loadKey(o.KeyFile)
		if err != nil {
			return nil, err
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if c, err := net.Dial("unix", sock); err == nil {
			auths = append(auths, ssh.PublicKeysCallback(agent.NewClient(c).Signers))
		}
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("no SSH credentials: set %s, pass --ssh-key, or run an ssh-agent", PasswordEnv)
	}
	return auths, nil
}

func loadKey(path string) (ssh.Signer, error) {
	b, err := os.ReadFile(expandHome(path))
	if err != nil {
		return nil, err
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w (encrypted keys are not supported by the native transport — use an ssh-agent)", path, err)
	}
	return s, nil
}

// hostKeyCallback verifies against ~/.ssh/known_hosts. A host that is already
// known and no longer matches is a hard failure — that is the protection worth
// keeping. A host that is simply unknown is accepted (and its fingerprint
// printed) unless --ssh-strict-host-key is set, because an air-gapped cluster
// rarely has its nodes in a laptop's known_hosts.
func hostKeyCallback(strict bool) (ssh.HostKeyCallback, error) {
	var known ssh.HostKeyCallback
	if home, err := os.UserHomeDir(); err == nil {
		path := filepath.Join(home, ".ssh", "known_hosts")
		if _, serr := os.Stat(path); serr == nil {
			if cb, kerr := knownhosts.New(path); kerr == nil {
				known = cb
			}
		}
	}
	if known == nil && strict {
		return nil, fmt.Errorf("--ssh-strict-host-key needs a readable ~/.ssh/known_hosts")
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if known != nil {
			err := known(hostname, remote, key)
			if err == nil {
				return nil
			}
			var kerr *knownhosts.KeyError
			// A KeyError with entries means the host IS known and the key differs.
			if ok := asKeyError(err, &kerr); ok && len(kerr.Want) > 0 {
				return fmt.Errorf("host key for %s does not match ~/.ssh/known_hosts — refusing to continue", hostname)
			}
		}
		if strict {
			return fmt.Errorf("host %s is not in ~/.ssh/known_hosts (--ssh-strict-host-key)", hostname)
		}
		fmt.Fprintf(os.Stderr, "! %s host key %s not in known_hosts — accepting for this run\n",
			hostname, ssh.FingerprintSHA256(key))
		return nil
	}, nil
}

func asKeyError(err error, target **knownhosts.KeyError) bool {
	if ke, ok := err.(*knownhosts.KeyError); ok {
		*target = ke
		return true
	}
	return false
}

// run executes a command on the node and returns its combined output.
func (n *nativeConn) run(ctx context.Context, cmd string) (string, error) {
	s, err := n.c.NewSession()
	if err != nil {
		return "", err
	}
	defer s.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Signal(ssh.SIGKILL)
		case <-done:
		}
	}()
	b, err := s.CombinedOutput(cmd)
	close(done)
	return string(b), err
}

// upload streams a local file to the node. It pipes into `cat` rather than
// implementing SCP: same result, far less protocol surface, and it works on any
// node with a shell.
func (n *nativeConn) upload(ctx context.Context, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	s, err := n.c.NewSession()
	if err != nil {
		return err
	}
	defer s.Close()
	s.Stdin = f
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = s.Signal(ssh.SIGKILL)
		case <-done:
		}
	}()
	out, err := s.CombinedOutput(fmt.Sprintf("cat > %q", remotePath))
	close(done)
	if err != nil {
		return fmt.Errorf("%v%s", err, tail(string(out)))
	}
	return nil
}

func (n *nativeConn) close() {
	if n.c != nil {
		_ = n.c.Close()
	}
}

// splitUserHost separates "user@host", falling back to the default user.
func splitUserHost(address, defUser string) (user, host string) {
	if u, h, ok := strings.Cut(address, "@"); ok {
		return u, h
	}
	return defUser, address
}

// withPort appends the SSH port when the address has none.
func withPort(host string, port int) string {
	if port <= 0 {
		port = 22
	}
	if strings.HasPrefix(host, "[") || strings.Count(host, ":") == 1 {
		return host // already host:port (or a bracketed IPv6 literal)
	}
	return net.JoinHostPort(host, fmt.Sprint(port))
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}
