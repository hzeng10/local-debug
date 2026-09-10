package provision

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func randomString() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func copySSH(ctx context.Context, r Runner, s SSH, local, dest string) error {
	if e := s.validate(); e != nil {
		return e
	}
	if !regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`).MatchString(dest) || strings.Contains(dest, "..") {
		return fmt.Errorf("remote path must be an absolute simple path")
	}
	a := []string{"scp", "-q", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=15"}
	if s.Port != 0 {
		a = append(a, "-P", strconv.Itoa(s.Port))
	}
	if s.IdentityFile != "" {
		a = append(a, "-i", s.IdentityFile)
	}
	if s.KnownHostsFile != "" {
		a = append(a, "-o", "UserKnownHostsFile="+s.KnownHostsFile)
	}
	if s.ProxyJump != "" {
		a = append(a, "-J", s.ProxyJump)
	}
	if _, e := r.Run(ctx, append(a, "--", local, s.Host+":"+dest), nil, nil); e != nil {
		return e
	}
	sum, e := fileSHA(local)
	if e != nil {
		return e
	}
	plain := s
	plain.Sudo = false
	b, e := runSSH(ctx, r, plain, []string{"sha256sum", dest}, nil)
	if e != nil {
		return e
	}
	if !strings.HasPrefix(string(b), sum+" ") {
		return fmt.Errorf("remote file checksum mismatch")
	}
	return nil
}
func (a *Admin) Distribute(ctx context.Context) error {
	arch := a.C.Nodes[0].Arch
	if a.C.ImageMode == "registry" {
		env := []string{}
		if a.C.Registry.ConfigFile != "" {
			dir := filepath.Join(a.C.WorkDir, "registry-auth")
			if e := secureDir(dir); e != nil {
				return e
			}
			b, e := os.ReadFile(a.C.Registry.ConfigFile)
			if e != nil {
				return e
			}
			if e = os.WriteFile(filepath.Join(dir, "config.json"), b, 0600); e != nil {
				return e
			}
			defer os.RemoveAll(dir)
			env = append(env, "DOCKER_CONFIG="+dir)
		}
		for _, im := range a.L.Images {
			if im.Platform != "linux/"+arch {
				continue
			}
			ref := strings.TrimSuffix(a.C.Registry.Prefix, "/") + "/" + im.Name + ":" + strings.TrimPrefix(im.Digest, "sha256:")[:16]
			b, e := a.R.Run(ctx, []string{tool(a.C.Bundle, "crane"), "digest", ref}, nil, env)
			if e == nil && strings.TrimSpace(string(b)) == im.Digest {
				continue
			}
			fmt.Fprintf(a.Log, "push image %s\n", im.Name)
			if _, e = a.R.Run(ctx, []string{tool(a.C.Bundle, "crane"), "push", filepath.Join(a.C.Bundle, im.File), ref}, nil, env); e != nil {
				return e
			}
			b, e = a.R.Run(ctx, []string{tool(a.C.Bundle, "crane"), "digest", ref}, nil, env)
			if e != nil || strings.TrimSpace(string(b)) != im.Digest {
				return fmt.Errorf("registry digest verification failed for %s", im.Name)
			}
		}
		return a.ensurePullSecret(ctx)
	}
	for _, n := range a.C.Nodes {
		fmt.Fprintf(a.Log, "import images on node %s (%s)\n", n.Name, n.Runtime)
		stage := "/tmp/devctl-import-" + randomString()[:16]
		plain := n.SSH
		plain.Sudo = false
		if _, e := runSSH(ctx, a.R, plain, []string{"mkdir", "-m", "700", stage}, nil); e != nil {
			return e
		}
		e := func() error {
			defer runSSH(context.Background(), a.R, plain, []string{"rm", "-rf", "--", stage}, nil)
			localCRI := filepath.Join(a.C.Bundle, "bin", "linux-"+n.Arch, "crictl")
			if e := copySSH(ctx, a.R, plain, localCRI, stage+"/crictl"); e != nil {
				return e
			}
			if _, e := runSSH(ctx, a.R, plain, []string{"chmod", "700", stage + "/crictl"}, nil); e != nil {
				return e
			}
			cri := []string{stage + "/crictl", "--runtime-endpoint", "unix://" + n.Socket, "--image-endpoint", "unix://" + n.Socket}
			if _, e := runSSH(ctx, a.R, n.SSH, append(append([]string{}, cri...), "info"), nil); e != nil {
				return fmt.Errorf("node %s actual CRI socket not usable", n.Name)
			}
			for _, im := range a.L.Images {
				if im.Platform != "linux/"+n.Arch {
					continue
				}
				target := imageRepository(im.Ref) + "@" + im.Digest
				inspect := func() bool {
					lookup := target
					if n.Runtime == "docker" {
						lookup = im.Ref
					}
					b, e := runSSH(ctx, a.R, n.SSH, append(append([]string{}, cri...), "inspecti", lookup, "-o", "json"), nil)
					if e != nil {
						return false
					}
					var m map[string]any
					if json.Unmarshal(b, &m) != nil {
						return false
					}
					id := str(at(m, "status", "id"))
					return id == im.ConfigDigest || id == im.Digest
				}
				if inspect() {
					continue
				}
				dst := stage + "/" + im.Name + ".tar"
				if e := copySSH(ctx, a.R, plain, filepath.Join(a.C.Bundle, im.File), dst); e != nil {
					return e
				}
				if n.Runtime == "containerd" {
					base := []string{n.Ctr, "--address", n.Socket, "--namespace", "k8s.io", "images"}
					if _, e := runSSH(ctx, a.R, n.SSH, append(append([]string{}, base...), "import", "--digests", dst), nil); e != nil {
						return e
					}
					if _, e := runSSH(ctx, a.R, n.SSH, append(append([]string{}, base...), "tag", "--force", im.Ref, target), nil); e != nil {
						return e
					}
				} else {
					if _, e := runSSH(ctx, a.R, n.SSH, []string{"docker", "load", "--input", dst}, nil); e != nil {
						return e
					}
					// Docker cannot tag a digest reference: use the immutable archive tag in this mode.
				}
				if n.Runtime == "docker" {
					b, e := runSSH(ctx, a.R, n.SSH, append(append([]string{}, cri...), "inspecti", im.Ref, "-o", "json"), nil)
					if e != nil {
						return e
					}
					var m map[string]any
					if json.Unmarshal(b, &m) != nil || str(at(m, "status", "id")) != im.ConfigDigest {
						return fmt.Errorf("node %s image %s failed CRI verification", n.Name, im.Name)
					}
				} else if !inspect() {
					return fmt.Errorf("node %s image %s failed CRI verification", n.Name, im.Name)
				}
			}
			return nil
		}()
		if e != nil {
			return e
		}
	}
	return nil
}
func (a *Admin) applyObject(ctx context.Context, m map[string]any) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	args := []string{a.C.Kubectl, "--kubeconfig", a.C.Kubeconfig, "--context", a.C.Context, "-n", a.C.Namespace, "apply", "--server-side", "--field-manager=devctl-admin", "-f", "-"}
	_, e = a.R.Run(ctx, args, b, nil)
	return e
}
func (a *Admin) ensurePullSecret(ctx context.Context) error {
	if a.C.Registry.PullSecret == "" {
		return nil
	}
	if a.C.Registry.ConfigFile == "" {
		_, e := a.get(ctx, a.C.Namespace, "secret", a.C.Registry.PullSecret)
		return e
	}
	b, e := os.ReadFile(a.C.Registry.ConfigFile)
	if e != nil {
		return e
	}
	existing, e := a.kube(ctx, "-n", a.C.Namespace, "get", "secret", a.C.Registry.PullSecret, "--ignore-not-found", "-o", "json")
	if e != nil {
		return e
	}
	if len(strings.TrimSpace(string(existing))) > 0 {
		var m map[string]any
		if e = json.Unmarshal(existing, &m); e != nil {
			return e
		}
		if str(at(m, "data", ".dockerconfigjson")) != base64.StdEncoding.EncodeToString(b) {
			return fmt.Errorf("existing pull Secret differs; explicit administrator rotation is required")
		}
		return nil
	}
	return a.applyObject(ctx, map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": a.C.Registry.PullSecret, "namespace": a.C.Namespace}, "type": "kubernetes.io/dockerconfigjson", "data": map[string]string{".dockerconfigjson": base64.StdEncoding.EncodeToString(b)}})
}
func (a *Admin) Credentials(ctx context.Context) (string, error) {
	b, e := a.kube(ctx, "-n", a.C.Namespace, "get", "secret", a.C.Credentials.Secret, "--ignore-not-found", "-o", "json")
	if e != nil {
		return "", e
	}
	var data map[string]string
	if len(strings.TrimSpace(string(b))) > 0 {
		var m struct {
			Data map[string]string `json:"data"`
		}
		if e = json.Unmarshal(b, &m); e != nil {
			return "", e
		}
		data = m.Data
	} else {
		if a.C.Credentials.Mode != "generate" {
			return "", fmt.Errorf("existing credential Secret absent")
		}
		p := filepath.Join(a.C.WorkDir, "credentials")
		if e = secureDir(p); e != nil {
			return "", e
		}
		keyPath := filepath.Join(p, "key.pem")
		// Keep a generated key until Secret creation succeeds, so a retry never silently rotates it.
		if _, e = os.Stat(keyPath); os.IsNotExist(e) {
			if _, e = a.R.Run(ctx, []string{tool(a.C.Bundle, "chisel"), "server", "--keygen", keyPath}, nil, nil); e != nil {
				return "", e
			}
		}
		key, e := os.ReadFile(keyPath)
		if e != nil {
			return "", e
		}
		users := map[string][]string{}
		for _, u := range a.C.Credentials.Users {
			users[u+":"+randomString()] = []string{"^socks$", "^" + regexp.QuoteMeta(a.C.ClusterDNS) + ":53$"}
		}
		ub, _ := json.Marshal(users)
		data = map[string]string{"key.pem": base64.StdEncoding.EncodeToString(key), "users.json": base64.StdEncoding.EncodeToString(ub)}
		secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": a.C.Credentials.Secret, "namespace": a.C.Namespace, "labels": map[string]string{"devctl.io/managed": "true"}}, "type": "Opaque", "data": data}
		sb, _ := json.Marshal(secret)
		// Create, never overwrite an existing key in a concurrent installation.
		args := []string{a.C.Kubectl, "--kubeconfig", a.C.Kubeconfig, "--context", a.C.Context, "create", "-f", "-"}
		if _, e = a.R.Run(ctx, args, sb, nil); e != nil {
			return "", e
		}
	}
	key, e := base64.StdEncoding.DecodeString(data["key.pem"])
	if e != nil {
		return "", e
	}
	fp, e := keyFingerprint(key)
	if e != nil {
		return "", e
	}
	ub, e := base64.StdEncoding.DecodeString(data["users.json"])
	if e != nil {
		return "", e
	}
	var users map[string][]string
	if e = json.Unmarshal(ub, &users); e != nil {
		return "", e
	}
	out := filepath.Join(a.C.WorkDir, "handoff")
	if e = secureDir(out); e != nil {
		return "", e
	}
	for _, u := range a.C.Credentials.Users {
		found := false
		for credential, rules := range users {
			if strings.HasPrefix(credential, u+":") {
				if !has(rules, "^socks$") {
					return "", fmt.Errorf("user %s lacks SOCKS rule", u)
				}
				if e = writeJSON(filepath.Join(out, u+".credentials.json"), map[string]string{"DEVCTL_GATEWAY_AUTH": credential}); e != nil {
					return "", e
				}
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("user %s absent from existing credentials; configure it explicitly before retrying", u)
		}
	}
	return fp, nil
}
func (a *Admin) render(ctx context.Context, name, chart string, values map[string]any) (string, error) {
	path := filepath.Join(a.C.WorkDir, name+".values.json")
	if e := writeJSON(path, values); e != nil {
		return "", e
	}
	b, e := a.helm(ctx, "template", name, chart, "-n", a.C.Namespace, "-f", path, "--kube-version", "1.34.0", "--post-renderer", tool(a.C.Bundle, "devctl"), "--post-renderer-args", "admin", "--post-renderer-args", "render-filter", "--post-renderer-args", "--config", "--post-renderer-args", filepath.Join(a.C.WorkDir, "renderer.json"))
	if e != nil {
		return "", e
	}
	policy := "IfNotPresent"
	if a.C.ImageMode == "nodes" {
		policy = "Never"
	}
	if e = auditManifests(b, a.ImageRefs(), policy); e != nil {
		return "", e
	}
	if e = os.WriteFile(filepath.Join(a.C.WorkDir, name+".rendered.yaml"), b, 0600); e != nil {
		return "", e
	}
	return path, nil
}
func (a *Admin) Apply(ctx context.Context) (result map[string]any, err error) {
	if err = a.Preflight(ctx); err != nil {
		return nil, err
	}
	g, m, err := a.Values(ctx)
	if err != nil {
		return nil, err
	}
	g["devctlManaged"] = true
	var profile map[string]any
	if e := readJSON(a.C.DeveloperProfile, &profile); e != nil {
		return nil, fmt.Errorf("developer profile: %w", e)
	}
	if profile == nil || profile["version"] != float64(1) {
		return nil, fmt.Errorf("developer profile must be a version 1 JSON object")
	}
	if a.C.Credentials.Mode == "existingSecret" {
		if _, _, e := a.readCredentials(ctx); e != nil {
			return nil, e
		}
	}

	if err = secureDir(a.C.WorkDir); err != nil {
		return nil, err
	}
	lockDir := filepath.Join(a.C.WorkDir, "apply.lock")
	if err = os.Mkdir(lockDir, 0700); err != nil {
		return nil, fmt.Errorf("deployment lock exists or cannot be created; inspect any existing operation before removing it")
	}
	defer os.Remove(lockDir)
	nodes := []string{}
	for _, n := range a.C.Nodes {
		nodes = append(nodes, n.Name)
	}
	policy := "IfNotPresent"
	if a.C.ImageMode == "nodes" {
		policy = "Never"
	}
	if err = writeJSON(filepath.Join(a.C.WorkDir, "renderer.json"), RenderConfig{Refs: a.ImageRefs(), Policy: policy, Nodes: nodes, Tolerations: a.C.Tolerations}); err != nil {
		return nil, err
	}
	gatewayChart := filepath.Join(a.C.Bundle, "deploy", "gateway")
	managerChart := filepath.Join(a.C.Bundle, "charts", "telepresence.tgz")
	gp, err := a.render(ctx, a.C.GatewayRelease, gatewayChart, g)
	if err != nil {
		return nil, err
	}
	mp := ""
	if has(a.C.Backends, "telepresence") {
		mp, err = a.render(ctx, a.C.ManagerRelease, managerChart, m)
		if err != nil {
			return nil, err
		}
	}
	oldG, err := a.release(ctx, a.C.GatewayRelease)
	if err != nil {
		return nil, err
	}
	oldM := Revision{}
	if mp != "" {
		oldM, err = a.managerState(ctx)
		if err != nil {
			return nil, err
		}
	}
	if oldG.Name != "" {
		b, e := a.helm(ctx, "get", "values", a.C.GatewayRelease, "-n", a.C.Namespace, "-o", "json")
		if e != nil {
			return nil, e
		}
		var v map[string]any
		_ = json.Unmarshal(b, &v)
		if v["devctlManaged"] != true {
			return nil, fmt.Errorf("existing gateway is not managed by this installer; no implicit adoption")
		}
	}
	changed := []Revision{}
	defer func() {
		if err == nil {
			return
		}
		recovery := []string{}
		for i := len(changed) - 1; i >= 0; i-- {
			r := changed[i]
			c, cancel := context.WithTimeout(context.Background(), time.Duration(a.C.TimeoutSeconds)*time.Second)
			var e error
			if r.Revision == 0 {
				_, e = a.helm(c, "uninstall", r.Name, "-n", a.C.Namespace, "--wait", "--timeout", strconv.Itoa(a.C.TimeoutSeconds)+"s")
			} else {
				_, e = a.helm(c, "rollback", r.Name, strconv.Itoa(r.Revision), "-n", a.C.Namespace, "--wait", "--timeout", strconv.Itoa(a.C.TimeoutSeconds)+"s")
			}
			cancel()
			if e != nil {
				recovery = append(recovery, r.Name+": recovery failed")
			} else {
				recovery = append(recovery, r.Name+": recovered")
			}
		}
		_ = writeJSON(filepath.Join(a.C.WorkDir, "failure.json"), map[string]any{"ok": false, "error": err.Error(), "recovery": recovery})
		if len(recovery) > 0 {
			err = fmt.Errorf("%w; recovery: %s", err, strings.Join(recovery, ", "))
		}
	}()
	if err = a.Distribute(ctx); err != nil {
		return nil, err
	}
	fp, err := a.Credentials(ctx)
	if err != nil {
		return nil, err
	}
	install := func(name, chart, path string, desired map[string]any, old Revision) error {
		if old.Name != "" {
			b, e := a.helm(ctx, "get", "values", name, "-n", a.C.Namespace, "-o", "json")
			if e != nil {
				return e
			}
			var current map[string]any
			if json.Unmarshal(b, &current) == nil {
				want, _ := json.Marshal(desired)
				got, _ := json.Marshal(current)
				if string(want) == string(got) {
					fmt.Fprintf(a.Log, "%s unchanged\n", name)
					return nil
				}
			}
		}
		previous := old
		previous.Name = name
		changed = append(changed, previous)
		_, e := a.helm(ctx, "upgrade", "--install", name, chart, "-n", a.C.Namespace, "-f", path, "--wait", "--timeout", strconv.Itoa(a.C.TimeoutSeconds)+"s", "--history-max", "5", "--post-renderer", tool(a.C.Bundle, "devctl"), "--post-renderer-args", "admin", "--post-renderer-args", "render-filter", "--post-renderer-args", "--config", "--post-renderer-args", filepath.Join(a.C.WorkDir, "renderer.json"))
		return e
	}
	if mp != "" && !a.C.ReuseManager {
		if err = install(a.C.ManagerRelease, managerChart, mp, m, oldM); err != nil {
			return nil, err
		}
	}
	if err = install(a.C.GatewayRelease, gatewayChart, gp, g, oldG); err != nil {
		return nil, err
	}
	result, err = a.Verify(ctx)
	if err != nil {
		return result, err
	}
	if err = a.Export(fp); err != nil {
		return result, err
	}
	result["handoff"] = filepath.Join(a.C.WorkDir, "handoff")
	result["fingerprint"] = fp
	configBytes, _ := json.Marshal(a.C)
	digest := sha256.Sum256(configBytes)
	result["configurationSHA256"] = hex.EncodeToString(digest[:])
	err = writeJSON(filepath.Join(a.C.WorkDir, "report.json"), result)
	return result, err
}
