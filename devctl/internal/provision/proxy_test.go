package provision

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Each child has a fresh Go environment-proxy cache, like a real CLI invocation.
func TestDownloadProxyChild(t *testing.T) {
	mode := os.Getenv("DEVCTL_PROXY_TEST_CHILD")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if mode == "runner" {
		b, err := (OSRunner{}).Run(ctx, []string{os.Args[0], "-test.run=^TestDownloadProxyChild$"}, nil, []string{"DEVCTL_PROXY_TEST_CHILD=fetch"})
		if err != nil {
			t.Fatalf("child tool: %v %s", err, b)
		}
		fmt.Print(string(b))
		return
	}
	cert, err := os.ReadFile(os.Getenv("DEVCTL_PROXY_TEST_CA"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(cert) {
		t.Fatal("test CA")
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: roots}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if strings.HasPrefix(addr, "example.com:") {
			if os.Getenv("DEVCTL_PROXY_TEST_DIRECT") != "1" {
				return nil, fmt.Errorf("unexpected direct connection")
			}
			addr = os.Getenv("DEVCTL_PROXY_TEST_ORIGIN")
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	http.DefaultTransport = tr
	var payload, log bytes.Buffer
	err = fetch(ctx, "https://example.com/start", &payload, &log)
	fmt.Print(log.String())
	if os.Getenv("DEVCTL_PROXY_TEST_FAILURE") == "1" {
		if err == nil || !strings.Contains(err.Error(), "Proxy Authentication Required") {
			t.Fatalf("expected actionable proxy failure, got %v", err)
		}
		if strings.Contains(err.Error(), "proxy-password") {
			t.Fatal("credentials in error")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if payload.String() != "verified-payload" {
		t.Fatalf("payload: %q", payload.String())
	}
}

func TestDownloadEnvironmentProxy(t *testing.T) {
	for _, tc := range []struct {
		name, variable, bypass, mode string
		failure                      bool
	}{
		{"uppercase", "HTTPS_PROXY", "", "fetch", false},
		{"lowercase", "https_proxy", "", "fetch", false},
		{"child-tool-inheritance", "HTTPS_PROXY", "", "runner", false},
		{"no-proxy-domain", "HTTPS_PROXY", "example.com", "fetch", false},
		{"no-proxy-wildcard", "HTTPS_PROXY", "*", "fetch", false},
		{"authentication-failure", "HTTPS_PROXY", "", "fetch", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/start" {
					http.Redirect(w, r, "https://example.com:8443/asset?signature=do-not-log", http.StatusFound)
					return
				}
				io.WriteString(w, "verified-payload")
			}))
			defer origin.Close()
			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var hosts []string
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				hosts = append(hosts, r.Host)
				mu.Unlock()
				if r.Method != http.MethodConnect {
					t.Errorf("method %s", r.Method)
					http.Error(w, "CONNECT required", 400)
					return
				}
				if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("proxy-user:proxy-password")) {
					t.Error("proxy credentials not forwarded correctly")
				}
				if tc.failure {
					http.Error(w, "Proxy Authentication Required", 407)
					return
				}
				upstream, err := net.DialTimeout("tcp", origin.Listener.Addr().String(), time.Second)
				if err != nil {
					t.Error(err)
					http.Error(w, "dial", 502)
					return
				}
				downstream, buf, err := w.(http.Hijacker).Hijack()
				if err != nil {
					upstream.Close()
					t.Error(err)
					return
				}
				defer downstream.Close()
				defer upstream.Close()
				downstream.SetDeadline(time.Now().Add(8 * time.Second))
				upstream.SetDeadline(time.Now().Add(8 * time.Second))
				buf.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
				buf.Flush()
				done := make(chan struct{})
				go func() { io.Copy(upstream, buf); upstream.Close(); close(done) }()
				io.Copy(downstream, upstream)
				downstream.Close()
				<-done
			}))
			defer proxy.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestDownloadProxyChild$")
			for _, e := range os.Environ() {
				key, _, _ := strings.Cut(e, "=")
				key = strings.ToUpper(key)
				if strings.HasSuffix(key, "_PROXY") || strings.HasPrefix(key, "DEVCTL_PROXY_TEST_") || key == "REQUEST_METHOD" {
					continue
				}
				cmd.Env = append(cmd.Env, e)
			}
			proxyURL := "http://proxy-user:proxy-password@" + strings.TrimPrefix(proxy.URL, "http://")
			cmd.Env = append(cmd.Env, tc.variable+"="+proxyURL, "NO_PROXY="+tc.bypass, "DEVCTL_PROXY_TEST_CHILD="+tc.mode, "DEVCTL_PROXY_TEST_CA="+ca, "DEVCTL_PROXY_TEST_ORIGIN="+origin.Listener.Addr().String())
			if tc.bypass != "" {
				cmd.Env = append(cmd.Env, "DEVCTL_PROXY_TEST_DIRECT=1")
			}
			if tc.failure {
				cmd.Env = append(cmd.Env, "DEVCTL_PROXY_TEST_FAILURE=1")
			}
			b, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, b)
			}
			if strings.Contains(string(b), "proxy-password") || strings.Contains(string(b), "proxy-user") || strings.Contains(string(b), "do-not-log") {
				t.Fatal("sensitive URL contents in diagnostics")
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.bypass != "" {
				if len(hosts) != 0 || !bytes.Contains(b, []byte("via direct")) {
					t.Fatalf("NO_PROXY ignored: %v %s", hosts, b)
				}
			} else {
				count := 2
				if tc.failure {
					count = 1
				}
				if len(hosts) != count || !bytes.Contains(b, []byte("via proxy http://")) {
					t.Fatalf("proxy/redirect not used: %v %s", hosts, b)
				}
			}
		})
	}
}
