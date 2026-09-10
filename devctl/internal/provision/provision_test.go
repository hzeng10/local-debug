package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type runFunc func(context.Context, []string, []byte, []string) ([]byte, error)

func (f runFunc) Run(c context.Context, a []string, b []byte, e []string) ([]byte, error) {
	return f(c, a, b, e)
}
func TestStrictJSONAndSafePaths(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	for _, s := range []string{`{"version":1,"oops":true}`, `{"version":1} {}`} {
		os.WriteFile(p, []byte(s), 0600)
		var c BundleConfig
		if readJSON(p, &c) == nil {
			t.Fatal(s)
		}
	}
	for _, p := range []string{"../escape", "a/../x", "/abs", "C:/file", "a\\b", "a//b", "a:stream"} {
		if _, e := relative(t.TempDir(), p); e == nil {
			t.Fatal(p)
		}
	}
}
func TestCNIExclusion(t *testing.T) {
	for _, tc := range []struct {
		s              string
		blocked, known bool
	}{{`{"plugins":[{"exclude_namespaces":["kube-system"]}]}`, true, true}, {`excludeNamespaces: []`, false, true}, {`hello: world`, false, false}} {
		blocked, known := CNIExcluded(map[string]any{"data": map[string]any{"cni_network_config": tc.s}}, "kube-system")
		if blocked != tc.blocked || known != tc.known {
			t.Fatal(tc)
		}
	}
}
func testKey(t *testing.T) []byte {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := x509.MarshalECPrivateKey(k)
	return []byte("ck-" + base64.RawStdEncoding.EncodeToString(b))
}
func TestKeyFingerprintAndArchive(t *testing.T) {
	fp, e := keyFingerprint(testKey(t))
	if e != nil || len(fp) != 44 {
		t.Fatal(fp, e)
	}
	if _, e = keyFingerprint([]byte("wrong")); e == nil {
		t.Fatal("invalid key accepted")
	}
	p := filepath.Join(t.TempDir(), "image.tar")
	f, _ := os.Create(p)
	w := tar.NewWriter(f)
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	for name, b := range map[string][]byte{"config.json": config, "manifest.json": []byte(`[{"Config":"config.json","RepoTags":["example/app:1"],"Layers":[]}]`)} {
		w.WriteHeader(&tar.Header{Name: name, Size: int64(len(b)), Mode: 0600})
		w.Write(b)
	}
	w.Close()
	f.Close()
	id, e := retagArchive(p, "devctl.local/app:0123456789abcdef-amd64")
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(config)
	if id != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatal(id)
	}
	f, _ = os.Open(p)
	defer f.Close()
	r := tar.NewReader(f)
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if h.Name == "manifest.json" {
			b, _ := io.ReadAll(r)
			if !bytes.Contains(b, []byte("devctl.local/app:0123456789abcdef-amd64")) {
				t.Fatal(string(b))
			}
		}
	}
}
func testInstall(t *testing.T) (Install, Lock) {
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	os.MkdirAll(bundle, 0700)
	l := Lock{Version: 1}
	for _, p := range []string{"bin/linux-amd64/devctl", "bin/linux-amd64/kubectl", "bin/linux-amd64/helm", "bin/linux-amd64/crane", "bin/linux-amd64/chisel", "bin/linux-amd64/istioctl", "bin/linux-amd64/crictl", "charts/telepresence.tgz"} {
		full := filepath.Join(bundle, p)
		os.MkdirAll(filepath.Dir(full), 0700)
		os.WriteFile(full, []byte("fixture"), 0700)
		h, _ := fileSHA(full)
		l.Files = append(l.Files, LockedFile{Path: p, SHA256: h})
	}
	for _, name := range []string{"chisel", "tel2", "busybox", "curl"} {
		p := "images/" + name + ".tar"
		full := filepath.Join(bundle, p)
		os.MkdirAll(filepath.Dir(full), 0700)
		os.WriteFile(full, []byte(name), 0600)
		h, _ := fileSHA(full)
		l.Files = append(l.Files, LockedFile{Path: p, SHA256: h})
		l.Images = append(l.Images, Image{Name: name, Source: "example/" + name + ":1", Platform: "linux/amd64", File: p, Digest: "sha256:" + strings.Repeat("a", 64), ConfigDigest: "sha256:" + strings.Repeat("b", 64), Ref: "devctl.local/" + name + ":0123456789abcdef-amd64"})
	}
	writeJSON(filepath.Join(bundle, "bundle.lock.json"), l)
	profile := filepath.Join(root, "profile.json")
	os.WriteFile(profile, []byte(`{"version":1,"name":"gde-adapter","cluster":{},"network":{},"source":{},"application":{}}`), 0600)
	c := Install{Version: 1, Namespace: "kube-system", Context: "test", Kubeconfig: "/config", Kubectl: "kubectl", Bundle: bundle, WorkDir: filepath.Join(root, "work"), ImageMode: "registry", Registry: Registry{Prefix: "registry.example/devctl"}, Nodes: []Node{{Name: "node-a", Arch: "amd64", SSH: SSH{Host: "node-a"}}}, Backends: []string{"telepresence", "gateway"}, ManagerRelease: "traffic-manager", GatewayRelease: "dev-egress", Credentials: Credentials{Mode: "generate", Secret: "dev-egress-auth", Users: []string{"alice"}}, BusinessDeployment: "gde-adapter", BusinessContainer: "gde-adapter", DeveloperProfile: profile, Domain: "cluster.local", ClusterDNS: "10.96.0.10", RouteCIDRs: []string{"10.96.0.0/12"}, FallbackDNS: "192.0.2.53", Dependencies: []Dependency{{Name: "redis", Namespace: "kube-system", Service: "redis", Port: 6379}}, IstioNamespace: "istio-system", CNIDaemonSet: "istio-cni-node", CNIConfigMap: "istio-cni-config", TimeoutSeconds: 30}
	return c, l
}
func TestBundleTampering(t *testing.T) {
	c, l := testInstall(t)
	if _, e := BundleVerify(c.Bundle); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(c.Bundle, l.Files[0].Path), []byte("changed"), 0600)
	if _, e := BundleVerify(c.Bundle); e == nil {
		t.Fatal("tamper accepted")
	}
}
func TestManifestAudit(t *testing.T) {
	refs := map[string]string{"chisel": "devctl.local/chisel:0123456789abcdef-amd64"}
	good := []byte(`{"kind":"Pod","spec":{"containers":[{"image":"devctl.local/chisel:0123456789abcdef-amd64","imagePullPolicy":"Never"}]}}`)
	if e := auditManifests(good, refs, "Never"); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(string(good), "Never", "Always", 1), strings.Replace(string(good), "devctl.local", "docker.io", 1), "kind: Namespace\nmetadata: {name: new}"} {
		if auditManifests([]byte(bad), refs, "Never") == nil {
			t.Fatal(bad)
		}
	}
}
func TestSSHQuoting(t *testing.T) {
	r := runFunc(func(_ context.Context, a []string, _ []byte, _ []string) ([]byte, error) {
		want := `exec 'sudo' '-n' '--' 'tool' 'a'"'"'b' '$(bad)'`
		if a[len(a)-1] != want {
			t.Fatal(a)
		}
		if !has(a, "StrictHostKeyChecking=yes") {
			t.Fatal(a)
		}
		return nil, nil
	})
	_, e := runSSH(context.Background(), r, SSH{Host: "node", Sudo: true}, []string{"tool", "a'b", "$(bad)"}, nil)
	if e != nil {
		t.Fatal(e)
	}
}
func TestRuntimeMustMatchKubelet(t *testing.T) {
	a := Admin{R: runFunc(func(_ context.Context, args []string, _ []byte, _ []string) ([]byte, error) {
		if strings.Contains(args[len(args)-1], "instance-config") {
			return []byte("containerRuntimeEndpoint: unix:///run/containerd/containerd.sock"), nil
		}
		return []byte("ok"), nil
	})}
	n := Node{Name: "n", SSH: SSH{Host: "n"}, Runtime: "docker"}
	if _, e := a.runtime(context.Background(), n, "unknown"); e == nil {
		t.Fatal("wrong runtime accepted")
	}
	n.Runtime = "auto"
	got, e := a.runtime(context.Background(), n, "unknown")
	if e != nil || got.Runtime != "containerd" || got.Socket != "/run/containerd/containerd.sock" {
		t.Fatal(got, e)
	}
}
func TestActualHelmCharts(t *testing.T) {
	helm := os.Getenv("DEVCTL_HELM_VALIDATOR")
	chart := os.Getenv("DEVCTL_TP_CHART")
	if helm == "" || chart == "" {
		t.Skip("set offline Helm and Telepresence chart validators")
	}
	patched := filepath.Join(t.TempDir(), "telepresence.tgz")
	if e := PatchChart(chart, patched); e != nil {
		t.Fatal(e)
	}
	chart = patched
	c, l := testInstall(t)
	c.Dependencies = nil
	for _, mode := range []string{"registry", "nodes"} {
		c.ImageMode = mode
		a := Admin{C: c, L: l, R: OSRunner{}}
		g, m, e := a.Values(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		for _, tc := range []struct {
			name, path string
			values     map[string]any
		}{{"dev-egress", "../../deploy/gateway", g}, {"traffic-manager", chart, m}} {
			p := filepath.Join(t.TempDir(), "values.json")
			writeJSON(p, tc.values)
			args := []string{"template", tc.name, tc.path, "-n", "kube-system", "-f", p, "--kube-version", "1.34.0"}
			if binary := os.Getenv("DEVCTL_EXECUTABLE_VALIDATOR"); binary != "" {
				policy := "IfNotPresent"
				if mode == "nodes" {
					policy = "Never"
				}
				renderer := filepath.Join(t.TempDir(), "renderer.json")
				writeJSON(renderer, RenderConfig{Refs: a.ImageRefs(), Policy: policy, Nodes: []string{"node-a"}})
				args = append(args, "--post-renderer", binary, "--post-renderer-args", "admin", "--post-renderer-args", "render-filter", "--post-renderer-args", "--config", "--post-renderer-args", renderer)
			}
			b, e := exec.Command(helm, args...).CombinedOutput()
			if e != nil {
				t.Fatalf("%s %s: %v\n%s", mode, tc.name, e, b)
			}
			policy := "IfNotPresent"
			if mode == "nodes" {
				policy = "Never"
			}
			if e = auditManifests(b, a.ImageRefs(), policy); e != nil {
				t.Fatalf("unfiltered hooks: %v", e)
			}
			b, e = FilterManifests(b, RenderConfig{Refs: a.ImageRefs(), Policy: policy, Nodes: []string{"node-a"}})
			if e != nil {
				t.Fatalf("%s %s: %v", mode, tc.name, e)
			}
			if !bytes.Contains(b, []byte("node-a")) {
				t.Fatal("missing scheduling boundary")
			}
			if tc.name == "traffic-manager" && !bytes.Contains(b, []byte("devctl.io/shared-egress")) {
				t.Fatal("missing webhook selector")
			}
		}
	}
}
func TestSOCKSFailure(t *testing.T) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	go func() {
		c, _ := l.Accept()
		defer c.Close()
		io.ReadFull(c, make([]byte, 3))
		c.Write([]byte{5, 0})
		b := make([]byte, 5)
		io.ReadFull(c, b)
		io.ReadFull(c, make([]byte, int(b[4])+2))
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
	}()
	if _, e = socksDial(context.Background(), l.Addr().String(), "redis.kube-system.svc.cluster.local", 6379); e == nil {
		t.Fatal("failure accepted")
	}
}

// Stateful fake cluster exercises apply/retry/rollback, including real credential
// generation, secret reuse, profile export and Helm value comparison.
type fakeCluster struct {
	t          *testing.T
	a          *Admin
	values     map[string]map[string]any
	secret     map[string]any
	upgrades   int
	uninstalls int
	blocked    bool
	key        []byte
}

func (f *fakeCluster) Run(ctx context.Context, args []string, in []byte, env []string) ([]byte, error) {
	s := strings.Join(args, " ")
	encode := func(v any) ([]byte, error) { return json.Marshal(v) }
	after := func(k string) string {
		for i, v := range args {
			if v == k && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	if strings.Contains(s, "chisel server --keygen") {
		return nil, os.WriteFile(args[len(args)-1], f.key, 0600)
	}
	if strings.Contains(s, "crane digest") {
		return []byte("sha256:" + strings.Repeat("a", 64)), nil
	}
	if strings.Contains(s, "helm") {
		if has(args, "template") {
			name := after("template")
			ref := f.a.ImageRefs()["chisel"]
			if name == "traffic-manager" {
				ref = f.a.ImageRefs()["tel2"]
			}
			return encode(map[string]any{"kind": "Deployment", "spec": map[string]any{"containers": []any{map[string]any{"image": ref, "imagePullPolicy": "IfNotPresent"}}}})
		}
		if has(args, "list") {
			name := strings.Trim(after("--filter"), "^$")
			if _, ok := f.values[name]; !ok {
				return []byte("[]"), nil
			}
			chart := "devctl-gateway-0.1.0"
			if name == "traffic-manager" {
				chart = "telepresence-oss-2.31.0"
			}
			return encode([]map[string]any{{"name": name, "revision": "1", "chart": chart, "status": "deployed"}})
		}
		if has(args, "get") {
			return encode(f.values[after("values")])
		}
		if has(args, "upgrade") {
			name := after("--install")
			var v map[string]any
			readJSON(after("-f"), &v)
			f.values[name] = v
			f.upgrades++
			return nil, nil
		}
		if has(args, "uninstall") {
			delete(f.values, after("uninstall"))
			f.uninstalls++
			return nil, nil
		}
		if has(args, "rollback") {
			return nil, nil
		}
	}
	if has(args, "version") {
		return encode(map[string]any{"serverVersion": map[string]any{"gitVersion": "v1.34.0"}})
	}
	if has(args, "can-i") {
		return []byte("yes"), nil
	}
	if has(args, "create") {
		json.Unmarshal(in, &f.secret)
		return nil, nil
	}
	if has(args, "rollout") {
		return []byte("ready"), nil
	}
	if has(args, "get") {
		kind := after("get")
		switch kind {
		case "namespace":
			return []byte(`{"metadata":{"name":"kube-system"}}`), nil
		case "nodes":
			return []byte(`{"items":[{"metadata":{"name":"node-a"},"status":{"nodeInfo":{"architecture":"amd64","containerRuntimeVersion":"containerd://1.7"}}}]}`), nil
		case "configmap":
			v := `{"exclude_namespaces":[]}`
			if f.blocked {
				v = `{"exclude_namespaces":["kube-system"]}`
			}
			return encode(map[string]any{"data": map[string]any{"cni_network_config": v}})
		case "daemonset":
			return []byte(`{"spec":{"template":{"spec":{"containers":[{"env":[{"name":"AMBIENT_ENABLED","value":"true"}]}]}}}}`), nil
		case "deployment":
			return []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"gde-adapter","env":[{"name":"PASSWORD","value":"do-not-export"}]}]}}}}`), nil
		case "secret":
			if f.secret == nil {
				return nil, nil
			}
			return encode(f.secret)
		case "services":
			return []byte(`{"items":[]}`), nil
		case "service":
			return []byte(`{"spec":{"selector":{"app":"redis"},"ports":[{"port":6379,"targetPort":6379}]}}`), nil
		case "pods":
			refs := f.a.ImageRefs()
			containers := []any{}
			for _, x := range []struct{ name, im string }{{"chisel", "chisel"}, {"traffic-agent", "tel2"}} {
				containers = append(containers, map[string]any{"name": x.name, "image": refs[x.im], "imagePullPolicy": "IfNotPresent"})
			}
			return encode(map[string]any{"items": []any{map[string]any{"metadata": map[string]any{"annotations": map[string]any{"ambient.istio.io/redirection": "enabled"}}, "spec": map[string]any{"nodeName": "node-a", "containers": containers}, "status": map[string]any{"phase": "Running", "containerStatuses": []any{map[string]any{"ready": true}}}}}})
		}
	}
	return nil, fmt.Errorf("unexpected fixture command %s", s)
}
func TestApplyIdempotencyAndRecovery(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			c, l := testInstall(t)
			a := Admin{C: c, L: l, Log: io.Discard}
			f := fakeCluster{t: t, a: &a, values: map[string]map[string]any{}, key: testKey(t)}
			a.R = &f
			a.Probe = func(context.Context, string, string) ([]map[string]any, error) {
				if failure {
					return nil, fmt.Errorf("dependency unreachable")
				}
				return []map[string]any{{"name": "redis", "dnsAndTCP": true}}, nil
			}
			_, e := a.Apply(context.Background())
			if failure {
				if e == nil || f.uninstalls != 2 || f.secret == nil {
					t.Fatal(e, f.uninstalls)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			first, _ := json.Marshal(f.secret)
			_, e = a.Apply(context.Background())
			if e != nil || f.upgrades != 2 {
				t.Fatal("not idempotent", e, f.upgrades)
			}
			second, _ := json.Marshal(f.secret)
			if !reflect.DeepEqual(first, second) {
				t.Fatal("credential rotation")
			}
			b, e := os.ReadFile(filepath.Join(c.WorkDir, "report.json"))
			if e != nil || bytes.Contains(b, []byte("DEVCTL_GATEWAY_AUTH")) {
				t.Fatal(e, string(b))
			}
			if _, e = os.Stat(filepath.Join(c.WorkDir, "handoff", "gde-adapter.ssh.json")); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestPreflightBlocksBeforeMutation(t *testing.T) {
	c, l := testInstall(t)
	a := Admin{C: c, L: l, Log: io.Discard}
	f := fakeCluster{t: t, a: &a, values: map[string]map[string]any{}, blocked: true}
	a.R = &f
	if _, e := a.Apply(context.Background()); e == nil {
		t.Fatal("excluded namespace accepted")
	}
	if f.secret != nil || f.upgrades != 0 {
		t.Fatal("preflight mutated cluster")
	}
}

func TestOfflineBundlePreparationFromPreloadedFiles(t *testing.T) {
	root := t.TempDir()
	dist := filepath.Join(root, "distribution")
	for _, dir := range []string{"deploy", "docs", "examples", "scripts", "dist"} {
		os.MkdirAll(filepath.Join(dist, dir), 0700)
	}
	for _, name := range []string{"README.md", "dist/devctl-linux-amd64", "dist/devctl-windows-amd64.exe"} {
		os.WriteFile(filepath.Join(dist, name), []byte("fixture binary"), 0700)
	}
	chart := filepath.Join(root, "upstream.tgz")
	var buf bytes.Buffer
	g := gzip.NewWriter(&buf)
	w := tar.NewWriter(g)
	for name, data := range map[string]string{"telepresence-oss/Chart.yaml": "apiVersion: v2\nname: telepresence-oss\nversion: 2.31.0\n", "telepresence-oss/templates/tests/test-connection.yaml": "spec:\n  containers:\n    - name: wget\n      command: ['wget']\n"} {
		w.WriteHeader(&tar.Header{Name: name, Size: int64(len(data)), Mode: 0600})
		w.Write([]byte(data))
	}
	w.Close()
	g.Close()
	os.WriteFile(chart, buf.Bytes(), 0600)
	chartSHA, _ := fileSHA(chart)
	input := filepath.Join(root, "tool")
	os.WriteFile(input, []byte("fixture"), 0700)
	toolSHA, _ := fileSHA(input)
	c := BundleConfig{Version: 1, Output: "out", Cache: "cache", Distribution: "distribution", Artifacts: []Artifact{{ID: "crane", File: "tool", SHA256: toolSHA, Target: "bin/linux-amd64/crane"}, {ID: "chart", File: "upstream.tgz", SHA256: chartSHA, Target: "charts/telepresence-upstream.tgz"}}, Images: []Image{{Name: "chisel", Source: "example/chisel:1", Platform: "linux/amd64", File: "images/chisel.tar"}}}
	path := filepath.Join(root, "bundle.json")
	writeJSON(path, c)
	r := runFunc(func(_ context.Context, args []string, _ []byte, _ []string) ([]byte, error) {
		if has(args, "digest") {
			return []byte("sha256:" + strings.Repeat("a", 64)), nil
		}
		if has(args, "pull") {
			f, e := os.Create(args[len(args)-1])
			if e != nil {
				return nil, e
			}
			w := tar.NewWriter(f)
			for name, b := range map[string][]byte{"config.json": []byte(`{"architecture":"amd64","os":"linux"}`), "manifest.json": []byte(`[{"Config":"config.json","RepoTags":["example/chisel:1"],"Layers":[]}]`)} {
				w.WriteHeader(&tar.Header{Name: name, Size: int64(len(b)), Mode: 0600})
				w.Write(b)
			}
			w.Close()
			f.Close()
			return nil, nil
		}
		return nil, fmt.Errorf("unexpected command")
	})
	lock, e := BundlePrepare(context.Background(), path, r, io.Discard)
	if e != nil {
		t.Fatal(e)
	}
	if len(lock.Images) != 1 || lock.Images[0].ConfigDigest == "" {
		t.Fatal(lock)
	}
	if _, e = BundleVerify(filepath.Join(root, "out")); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(root, "out", "unexpected.tpl"), []byte("extra"), 0600)
	if _, e = BundleVerify(filepath.Join(root, "out")); e == nil {
		t.Fatal("extra bundle file accepted")
	}
}
func TestNodeImportRejectsWrongRuntimeContent(t *testing.T) {
	c, l := testInstall(t)
	c.ImageMode = "nodes"
	c.Nodes[0].Runtime = "containerd"
	c.Nodes[0].Socket = "/run/containerd/containerd.sock"
	c.Nodes[0].Ctr = "ctr"
	sent := map[string]string{}
	imports := 0
	r := runFunc(func(_ context.Context, args []string, _ []byte, _ []string) ([]byte, error) {
		if args[0] == "scp" {
			sum, e := fileSHA(args[len(args)-2])
			if e != nil {
				return nil, e
			}
			dest := strings.SplitN(args[len(args)-1], ":", 2)[1]
			sent[dest] = sum
			return nil, nil
		}
		cmd := args[len(args)-1]
		if strings.Contains(cmd, "'sha256sum'") {
			for dest, sum := range sent {
				if strings.Contains(cmd, quote(dest)) {
					return []byte(sum + "  " + dest + "\n"), nil
				}
			}
			return nil, fmt.Errorf("unknown copied file")
		}
		if strings.Contains(cmd, "'inspecti'") {
			return []byte(`{"status":{"id":"sha256:wrong"}}`), nil
		}
		if strings.Contains(cmd, "'import'") {
			imports++
		}
		return nil, nil
	})
	a := Admin{C: c, L: l, R: r, Log: io.Discard}
	if e := a.Distribute(context.Background()); e == nil {
		t.Fatal("incorrect CRI image accepted")
	}
	if imports != 1 {
		t.Fatal("did not stop after first failed image", imports)
	}
}

func TestReportArchiveRoundTrip(t *testing.T) {
	root := t.TempDir()
	handoff := filepath.Join(root, "handoff")
	os.MkdirAll(handoff, 0700)
	os.WriteFile(filepath.Join(handoff, "alice.credentials.json"), []byte(`{"DEVCTL_GATEWAY_AUTH":"alice:secret"}`), 0600)
	archive := filepath.Join(t.TempDir(), "report.tgz")
	if e := packTree(root, archive); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(archive)
	out := t.TempDir()
	if e := unpackReport(b, out); e != nil {
		t.Fatal(e)
	}
	got, e := os.ReadFile(filepath.Join(out, "handoff", "alice.credentials.json"))
	if e != nil || !bytes.Contains(got, []byte("alice:secret")) {
		t.Fatal(e)
	}
	var buf bytes.Buffer
	g := gzip.NewWriter(&buf)
	w := tar.NewWriter(g)
	w.WriteHeader(&tar.Header{Name: "../outside", Mode: 0600, Size: 1})
	w.Write([]byte("x"))
	w.Close()
	g.Close()
	if unpackReport(buf.Bytes(), out) == nil {
		t.Fatal("unsafe report path accepted")
	}
}

func TestDiscoverOmitsInlineValues(t *testing.T) {
	c, l := testInstall(t)
	a := Admin{C: c, L: l, Log: io.Discard}
	f := fakeCluster{t: t, a: &a, values: map[string]map[string]any{}}
	a.R = &f
	out, e := a.Discover(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(out)
	if bytes.Contains(b, []byte("do-not-export")) {
		t.Fatal("inline value leaked")
	}
	if out["installDraft"] == nil {
		t.Fatal("draft missing")
	}
}
