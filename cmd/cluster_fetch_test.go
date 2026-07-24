package cmd

import (
	"strings"
	"testing"
)

// A self-contained node-side kubeconfig, as `kubectl config view --raw --flatten
// --minify` would emit it (certs inlined, one cluster, current-context set).
const nodeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: prod
  cluster:
    server: https://172.25.71.121:6443
    certificate-authority-data: Q0FEQVRB
contexts:
- name: prod
  context:
    cluster: prod
    user: admin
current-context: prod
users:
- name: admin
  user:
    client-certificate-data: Q0VSVA==
    client-key-data: S0VZ
`

func TestRewriteKubeconfigForTunnel(t *testing.T) {
	tk, err := rewriteKubeconfigForTunnel([]byte(nodeKubeconfig), 6443, "", false)
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := string(tk.Raw)

	if tk.OrigServer != "https://172.25.71.121:6443" || tk.OrigHost != "172.25.71.121" || tk.OrigPort != "6443" {
		t.Errorf("original endpoint not captured: %+v", tk)
	}
	if tk.Server != "https://127.0.0.1:6443" || !strings.Contains(out, "server: https://127.0.0.1:6443") {
		t.Errorf("server not rewritten to the local tunnel end: %q", out)
	}
	// TLS must stay VERIFIED: pin the original host as SNI/verify name, keep the CA.
	if !strings.Contains(out, "tls-server-name: 172.25.71.121") {
		t.Errorf("tls-server-name not defaulted to the original host: %q", out)
	}
	if !strings.Contains(out, "certificate-authority-data: Q0FEQVRB") {
		t.Errorf("CA data must be preserved: %q", out)
	}
	if strings.Contains(out, "insecure-skip-tls-verify") {
		t.Errorf("default mode must not skip TLS verification: %q", out)
	}
	// Client credentials ride along untouched.
	if !strings.Contains(out, "client-certificate-data: Q0VSVA==") || !strings.Contains(out, "client-key-data: S0VZ") {
		t.Errorf("inline client credentials lost: %q", out)
	}
	if len(tk.Warnings) != 0 {
		t.Errorf("unexpected warnings: %v", tk.Warnings)
	}
}

func TestRewriteKubeconfigForTunnelOverrides(t *testing.T) {
	// Explicit tls-server-name override ("kubernetes" is on every standard cert).
	tk, err := rewriteKubeconfigForTunnel([]byte(nodeKubeconfig), 16443, "kubernetes", false)
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if !strings.Contains(string(tk.Raw), "tls-server-name: kubernetes") {
		t.Errorf("tls-server-name override not applied: %q", tk.Raw)
	}
	if !strings.Contains(string(tk.Raw), "server: https://127.0.0.1:16443") {
		t.Errorf("--local-port not applied: %q", tk.Raw)
	}

	// --insecure: skip verification, and the CA must be dropped (client-go rejects both).
	tk, err = rewriteKubeconfigForTunnel([]byte(nodeKubeconfig), 6443, "", true)
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	out := string(tk.Raw)
	if !strings.Contains(out, "insecure-skip-tls-verify: true") {
		t.Errorf("--insecure not applied: %q", out)
	}
	if strings.Contains(out, "certificate-authority-data") || strings.Contains(out, "tls-server-name") {
		t.Errorf("--insecure must drop CA data and tls-server-name: %q", out)
	}
}

func TestRewriteKubeconfigForTunnelDefaultPort(t *testing.T) {
	kc := strings.Replace(nodeKubeconfig, "https://172.25.71.121:6443", "https://api.cluster.internal", 1)
	tk, err := rewriteKubeconfigForTunnel([]byte(kc), 6443, "", false)
	if err != nil {
		t.Fatalf("rewrite failed: %v", err)
	}
	if tk.OrigHost != "api.cluster.internal" || tk.OrigPort != "443" {
		t.Errorf("https default port not applied: %+v", tk)
	}
}

func TestRewriteKubeconfigForTunnelRejectsFileRefs(t *testing.T) {
	caFile := strings.Replace(nodeKubeconfig,
		"certificate-authority-data: Q0FEQVRB", "certificate-authority: /etc/kubernetes/pki/ca.crt", 1)
	if _, err := rewriteKubeconfigForTunnel([]byte(caFile), 6443, "", false); err == nil ||
		!strings.Contains(err.Error(), "--flatten") {
		t.Errorf("CA file reference must be rejected with the --flatten hint, got: %v", err)
	}

	certFile := strings.Replace(nodeKubeconfig,
		"client-certificate-data: Q0VSVA==", "client-certificate: /var/lib/kubelet/pki/kubelet-client.crt", 1)
	if _, err := rewriteKubeconfigForTunnel([]byte(certFile), 6443, "", false); err == nil ||
		!strings.Contains(err.Error(), "--flatten") {
		t.Errorf("client-cert file reference must be rejected with the --flatten hint, got: %v", err)
	}
}

func TestRewriteKubeconfigForTunnelClusterSelection(t *testing.T) {
	// No usable current-context but exactly one cluster → use it.
	oneCluster := strings.Replace(nodeKubeconfig, "current-context: prod", "current-context: gone", 1)
	if _, err := rewriteKubeconfigForTunnel([]byte(oneCluster), 6443, "", false); err != nil {
		t.Errorf("single-cluster fallback should work: %v", err)
	}

	// Ambiguous (two clusters, no current-context match) → error pointing at --minify.
	two := strings.Replace(oneCluster, "- name: prod\n  cluster:\n    server: https://172.25.71.121:6443",
		"- name: prod\n  cluster:\n    server: https://172.25.71.121:6443\n- name: other\n  cluster:\n    server: https://10.0.0.9:6443", 1)
	if _, err := rewriteKubeconfigForTunnel([]byte(two), 6443, "", false); err == nil ||
		!strings.Contains(err.Error(), "--minify") {
		t.Errorf("ambiguous cluster must error with the --minify hint, got: %v", err)
	}

	if _, err := rewriteKubeconfigForTunnel([]byte("not: a kubeconfig at all"), 6443, "", false); err == nil {
		t.Errorf("garbage input must error")
	}
}
