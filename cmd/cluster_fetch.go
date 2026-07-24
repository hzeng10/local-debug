package cmd

// fetch-kubeconfig covers the topology where the laptop cannot obtain a kubeconfig
// by hand but CAN SSH to a cluster node where kubectl already works. The node's own
// credentials are dumped self-contained (`kubectl config view --raw --flatten
// --minify` inlines every cert), the server is rewritten to the laptop end of an
// 'ssh -L' tunnel to the apiserver, and TLS verification is preserved with
// tls-server-name instead of being skipped. The result is a kubeconfig client-go
// AND telepresence can use from the laptop: the intercept reaches the
// traffic-manager through an apiserver port-forward, and 'ssh -L' is a transparent
// TCP pipe that carries the stream upgrade ('ldbg cluster probe' verifies).

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
)

// tunneledKubeconfig is the result of rewriting a node-side kubeconfig so it works
// through a local 'ssh -L' tunnel.
type tunneledKubeconfig struct {
	Raw        []byte   // rewritten kubeconfig, ready to write to disk
	OrigServer string   // server URL as the node sees it
	OrigHost   string   // host the ssh tunnel must forward to
	OrigPort   string   // port the ssh tunnel must forward to
	Server     string   // rewritten local server URL
	Warnings   []string // non-fatal portability notes (e.g. exec credential plugins)
}

// rewriteKubeconfigForTunnel points the kubeconfig's cluster at 127.0.0.1:localPort
// and keeps TLS verification working: the apiserver cert was never issued for
// 127.0.0.1, so tls-server-name pins verification to the original host (or an
// explicit override such as "kubernetes"), against the CA the file already carries.
// Credentials referenced as file paths are rejected — they exist only on the node.
func rewriteKubeconfigForTunnel(raw []byte, localPort int, tlsServerName string, insecure bool) (*tunneledKubeconfig, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("not a kubeconfig: %w", err)
	}

	// Resolve the cluster the current context points at (after --minify there is
	// exactly one, but stay correct for hand-assembled files).
	name := ""
	if ctx, ok := cfg.Contexts[cfg.CurrentContext]; ok {
		name = ctx.Cluster
	}
	if _, ok := cfg.Clusters[name]; !ok {
		if len(cfg.Clusters) == 1 {
			for n := range cfg.Clusters {
				name = n
			}
		} else {
			return nil, fmt.Errorf("cannot pick a cluster (current-context %q, %d clusters) — dump with 'kubectl config view --raw --flatten --minify'",
				cfg.CurrentContext, len(cfg.Clusters))
		}
	}
	cl := cfg.Clusters[name]

	u, err := url.Parse(cl.Server)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("cluster %q has no parsable server URL (%q)", name, cl.Server)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	res := &tunneledKubeconfig{OrigServer: cl.Server, OrigHost: host, OrigPort: port}

	if cl.CertificateAuthority != "" {
		return nil, fmt.Errorf("cluster %q references a CA file (%s) that only exists on the node — dump with 'kubectl config view --raw --flatten --minify' to inline it",
			name, cl.CertificateAuthority)
	}
	for un, ai := range cfg.AuthInfos {
		if ai.ClientCertificate != "" || ai.ClientKey != "" || ai.TokenFile != "" {
			return nil, fmt.Errorf("user %q references credential files that only exist on the node — dump with 'kubectl config view --raw --flatten --minify' to inline them", un)
		}
		if ai.Exec != nil {
			res.Warnings = append(res.Warnings,
				fmt.Sprintf("user %q authenticates via an exec plugin (%s) — that binary must also exist on the laptop", un, ai.Exec.Command))
		}
	}

	cl.Server = fmt.Sprintf("%s://127.0.0.1:%d", u.Scheme, localPort)
	res.Server = cl.Server
	switch {
	case insecure:
		cl.InsecureSkipTLSVerify = true
		cl.CertificateAuthorityData = nil // client-go rejects CA + insecure together
		cl.TLSServerName = ""
	case u.Scheme == "https":
		if strings.TrimSpace(tlsServerName) == "" {
			tlsServerName = host
		}
		cl.TLSServerName = tlsServerName
	}

	outBytes, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("re-serialize kubeconfig: %w", err)
	}
	res.Raw = outBytes
	return res, nil
}

var (
	fkSSH       string
	fkRemoteCmd string
	fkIn        string
	fkLocalPort int
	fkOut       string
	fkTLSName   string
	fkInsecure  bool
)

var clusterFetchKubeconfigCmd = &cobra.Command{
	Use:   "fetch-kubeconfig",
	Short: "Pull working credentials from a cluster node over SSH and rewrite them for an 'ssh -L' tunnel",
	Long: `fetch-kubeconfig is for the topology where the laptop has no kubeconfig and cannot
reach the apiserver, but CAN SSH to a cluster node where kubectl works. It

  1. dumps the node's credentials self-contained over SSH
     (default: kubectl config view --raw --flatten --minify — inlines every cert),
  2. rewrites server: to https://127.0.0.1:<local-port> (the laptop end of an
     'ssh -L' tunnel to the apiserver), keeping full TLS verification via
     tls-server-name (the apiserver cert was never issued for 127.0.0.1),
  3. writes the result 0600 and prints the exact 'ssh -L' + verify commands.

With the tunnel up, this kubeconfig carries EVERYTHING client-go can do — including
port-forward and a full telepresence intercept ('ldbg up'): telepresence reaches the
traffic-manager through an apiserver port-forward, and 'ssh -L' is a transparent TCP
pipe. Confirm per-environment with 'ldbg cluster probe --kubeconfig <file>'.

If the default dump fails on the node (kubectl works only via sudo / a root-owned
file), retry with one of:
  --remote-cmd "sudo cat /etc/kubernetes/admin.conf"        (kubeadm)
  --remote-cmd "sudo cat /etc/rancher/rke2/rke2.yaml"       (RKE2)
  --remote-cmd "sudo cat /etc/rancher/k3s/k3s.yaml"         (k3s)

The output file contains real cluster credentials: it is written 0600, never printed,
and *.kubeconfig is gitignored. Treat it like a password.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		var raw []byte
		src := ""
		switch {
		case strings.TrimSpace(fkIn) != "":
			b, err := os.ReadFile(fkIn)
			if err != nil {
				return out.Failf("cluster fetch-kubeconfig", "", err)
			}
			raw, src = b, fkIn
		case strings.TrimSpace(fkSSH) != "":
			if _, err := exec.LookPath("ssh"); err != nil {
				return out.Failf("cluster fetch-kubeconfig",
					"no 'ssh' on PATH — Windows 10/11 ship the OpenSSH client (Settings → System → Optional features → OpenSSH Client)", err)
			}
			c := exec.Command("ssh", fkSSH, fkRemoteCmd)
			c.Stdin = os.Stdin   // let ssh prompt for passwords / host-key confirmation
			c.Stderr = os.Stderr // surface ssh/kubectl errors as they happen
			b, err := c.Output()
			if err != nil {
				return out.Failf("cluster fetch-kubeconfig",
					fmt.Sprintf("'ssh %s \"%s\"' failed — if the node needs root to read the kubeconfig, retry with --remote-cmd \"sudo cat /etc/kubernetes/admin.conf\" (kubeadm) or the RKE2/k3s path (see --help)", fkSSH, fkRemoteCmd),
					err)
			}
			raw, src = b, fkSSH
		default:
			return out.Failf("cluster fetch-kubeconfig",
				"pass --ssh user@node (dump over SSH) or --in FILE (rewrite an already-copied kubeconfig)",
				fmt.Errorf("--ssh or --in is required"))
		}

		tk, err := rewriteKubeconfigForTunnel(raw, fkLocalPort, fkTLSName, fkInsecure)
		if err != nil {
			return out.Failf("cluster fetch-kubeconfig", "", err)
		}
		if err := os.WriteFile(fkOut, tk.Raw, 0600); err != nil {
			return out.Failf("cluster fetch-kubeconfig", "", err)
		}

		sshTarget := strings.TrimSpace(fkSSH)
		if sshTarget == "" {
			sshTarget = "user@node"
		}
		sshLine := fmt.Sprintf("ssh -N -L %d:%s:%s %s", fkLocalPort, tk.OrigHost, tk.OrigPort, sshTarget)
		psArgs := fmt.Sprintf("'-N','-L','%d:%s:%s','%s'", fkLocalPort, tk.OrigHost, tk.OrigPort, sshTarget)

		tlsNote := "TLS verified via tls-server-name against the original CA"
		if fkInsecure {
			tlsNote = "insecure-skip-tls-verify (CA check DISABLED — prefer the default)"
		} else if !strings.HasPrefix(tk.Server, "https://") {
			tlsNote = "plain http endpoint, no TLS"
		}

		warn := ""
		for _, w := range tk.Warnings {
			warn += "  ! " + w + "\n"
		}

		human := fmt.Sprintf(`fetched kubeconfig from %s
  original server : %s
  rewritten server: %s (%s)
  wrote           : %s (0600 — contains real credentials, keep it private)
%s
next:
  1. tunnel :  %s
     PowerShell background:  Start-Process ssh -ArgumentList %s
  2. verify :  ldbg cluster probe --kubeconfig %s
  3. use    :  ldbg --kubeconfig %s sync <service>
     a full intercept works through this too: set KUBECONFIG=%s then 'ldbg up ...'
     (telepresence rides an apiserver port-forward; the probe's port-forward stage
     is the go/no-go check for this bridge)`,
			src, tk.OrigServer, tk.Server, tlsNote, fkOut, warn,
			sshLine, psArgs, fkOut, fkOut, fkOut)

		out.Result("cluster fetch-kubeconfig", human, map[string]any{
			"path":       fkOut,
			"server":     tk.Server,
			"origServer": tk.OrigServer,
			"sshTunnel":  sshLine,
			"warnings":   tk.Warnings,
		})
		return nil
	},
}

func init() {
	f := clusterFetchKubeconfigCmd.Flags()
	f.StringVar(&fkSSH, "ssh", "", "SSH target of a cluster node where kubectl works (user@node); uses the local OpenSSH client")
	f.StringVar(&fkRemoteCmd, "remote-cmd", "kubectl config view --raw --flatten --minify",
		"command run on the node to dump a self-contained kubeconfig")
	f.StringVar(&fkIn, "in", "", "rewrite an already-copied kubeconfig file instead of dumping over SSH")
	f.IntVar(&fkLocalPort, "local-port", 6443, "laptop port the 'ssh -L' tunnel will listen on")
	f.StringVar(&fkOut, "out", "tunnel.kubeconfig", "output file (written 0600)")
	f.StringVar(&fkTLSName, "tls-server-name", "", "override the TLS server name (default: the original apiserver host; 'kubernetes' also works on standard certs)")
	f.BoolVar(&fkInsecure, "insecure", false, "skip TLS verification instead of pinning tls-server-name (avoid unless the CA is unusable)")

	clusterCmd.AddCommand(clusterFetchKubeconfigCmd)
}
