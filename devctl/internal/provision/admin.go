package provision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

type Admin struct {
	C     Install
	L     Lock
	R     Runner
	Log   io.Writer
	Probe func(context.Context, string, string) ([]map[string]any, error)
}

func (a *Admin) kube(ctx context.Context, args ...string) ([]byte, error) {
	return a.R.Run(ctx, append([]string{a.C.Kubectl, "--kubeconfig", a.C.Kubeconfig, "--context", a.C.Context, "--request-timeout=30s"}, args...), nil, nil)
}
func (a *Admin) get(ctx context.Context, ns, kind, name string) (map[string]any, error) {
	args := []string{"-n", ns, "get", kind}
	if name != "" {
		args = append(args, name)
	}
	args = append(args, "-o", "json")
	b, e := a.kube(ctx, args...)
	if e != nil {
		return nil, fmt.Errorf("read %s/%s in %s: %w", kind, name, ns, e)
	}
	var m map[string]any
	e = json.Unmarshal(b, &m)
	return m, e
}
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}
func arr(v any) []any  { s, _ := v.([]any); return s }
func str(v any) string { s, _ := v.(string); return s }
func at(m map[string]any, keys ...string) any {
	var v any = m
	for _, k := range keys {
		v = obj(v)[k]
	}
	return v
}
func (a *Admin) helm(ctx context.Context, args ...string) ([]byte, error) {
	return a.R.Run(ctx, append([]string{tool(a.C.Bundle, "helm"), "--kubeconfig", a.C.Kubeconfig, "--kube-context", a.C.Context}, args...), nil, nil)
}
func (a *Admin) Discover(ctx context.Context) (map[string]any, error) {
	nodes, e := a.get(ctx, "", "nodes", "")
	if e != nil {
		return nil, e
	}
	out := map[string]any{"namespace": a.C.Namespace, "nodes": []any{}}
	for _, v := range arr(nodes["items"]) {
		n := obj(v)
		out["nodes"] = append(out["nodes"].([]any), map[string]any{"name": at(n, "metadata", "name"), "architecture": at(n, "status", "nodeInfo", "architecture"), "runtime": at(n, "status", "nodeInfo", "containerRuntimeVersion"), "addresses": at(n, "status", "addresses"), "podCIDRs": at(n, "spec", "podCIDRs")})
	}
	services, e := a.get(ctx, a.C.Namespace, "services", "")
	if e != nil {
		return nil, e
	}
	ss := []any{}
	for _, v := range arr(services["items"]) {
		m := obj(v)
		ss = append(ss, map[string]any{"name": at(m, "metadata", "name"), "clusterIP": at(m, "spec", "clusterIP"), "ports": at(m, "spec", "ports"), "selector": at(m, "spec", "selector")})
	}
	out["services"] = ss
	dep, e := a.get(ctx, a.C.Namespace, "deployment", a.C.BusinessDeployment)
	if e != nil {
		return nil, e
	}
	cs := []any{}
	for _, v := range arr(at(dep, "spec", "template", "spec", "containers")) {
		c := obj(v)
		keys := []string{}
		refs := []any{}
		for _, v := range arr(c["env"]) {
			ev := obj(v)
			keys = append(keys, str(ev["name"]))
			if x := ev["valueFrom"]; x != nil {
				refs = append(refs, x)
			}
		}
		cs = append(cs, map[string]any{"name": c["name"], "envNames": keys, "envFrom": c["envFrom"], "valueFrom": refs, "volumeMounts": c["volumeMounts"]})
	}
	volumes := []map[string]any{}
	for _, raw := range arr(at(dep, "spec", "template", "spec", "volumes")) {
		v := obj(raw)
		safe := map[string]any{"name": v["name"]}
		if name := at(v, "secret", "secretName"); name != nil {
			safe["secretName"] = name
		}
		if name := at(v, "configMap", "name"); name != nil {
			safe["configMapName"] = name
		}
		if name := at(v, "persistentVolumeClaim", "claimName"); name != nil {
			safe["persistentVolumeClaim"] = name
		}
		if v["csi"] != nil {
			safe["requiresExplicitMapping"] = "CSI"
		}
		if v["projected"] != nil {
			safe["requiresExplicitMapping"] = "projected"
		}
		volumes = append(volumes, safe)
	}
	out["business"] = map[string]any{"deployment": a.C.BusinessDeployment, "containers": cs, "volumes": volumes}
	// A draft contains references only. Applying it remains an explicit administrator action.
	draft := a.C
	configNames := map[string]bool{}
	secretNames := map[string]bool{}
	for _, raw := range arr(at(dep, "spec", "template", "spec", "containers")) {
		c := obj(raw)
		if str(c["name"]) != a.C.BusinessContainer {
			continue
		}
		for _, raw := range arr(c["envFrom"]) {
			v := obj(raw)
			if n := str(at(v, "configMapRef", "name")); n != "" {
				configNames[n] = true
			}
			if n := str(at(v, "secretRef", "name")); n != "" {
				secretNames[n] = true
			}
		}
		for _, raw := range arr(c["env"]) {
			v := obj(raw)
			if n := str(at(v, "valueFrom", "configMapKeyRef", "name")); n != "" {
				configNames[n] = true
			}
			if n := str(at(v, "valueFrom", "secretKeyRef", "name")); n != "" {
				secretNames[n] = true
			}
		}
	}
	for _, v := range volumes {
		if n := str(v["configMapName"]); n != "" {
			configNames[n] = true
		}
		if n := str(v["secretName"]); n != "" {
			secretNames[n] = true
		}
	}
	draft.ConfigMaps = []string{}
	for n := range configNames {
		draft.ConfigMaps = append(draft.ConfigMaps, n)
	}
	sort.Strings(draft.ConfigMaps)
	draft.Secrets = []string{}
	for n := range secretNames {
		draft.Secrets = append(draft.Secrets, n)
	}
	sort.Strings(draft.Secrets)
	out["installDraft"] = draft
	out["draftNotes"] = []string{"review nodes/SSH, business config references, middleware ports and network policy before apply", "no environment or Secret values included"}
	// Inline environment values, annotations and Secret bodies are never included.
	return out, nil
}
func (a *Admin) Preflight(ctx context.Context) error {
	if e := a.C.validate(); e != nil {
		return e
	}
	l, e := BundleVerify(a.C.Bundle)
	if e != nil {
		return e
	}
	a.L = l
	required := []string{"devctl", "helm", "crane", "chisel", "istioctl", "crictl", "kubectl"}
	for _, t := range required {
		p := tool(a.C.Bundle, t)
		rel, _ := filepath.Rel(a.C.Bundle, p)
		if !a.locked(filepath.ToSlash(rel)) {
			return fmt.Errorf("tool not locked in bundle: %s", t)
		}
	}
	if !a.locked("charts/telepresence.tgz") {
		return fmt.Errorf("Telepresence chart missing from lock")
	}
	version, e := a.kube(ctx, "version", "-o", "json")
	if e != nil {
		return e
	}
	var v map[string]any
	if e = json.Unmarshal(version, &v); e != nil {
		return e
	}
	if !strings.HasPrefix(str(at(v, "serverVersion", "gitVersion")), "v1.34.") {
		return fmt.Errorf("this profile targets Kubernetes 1.34.x")
	}
	if _, e = a.get(ctx, "", "namespace", a.C.Namespace); e != nil {
		return e
	}
	// Mesh exclusion is a hard precondition, not an implicit permission to upgrade CNI.
	cm, e := a.get(ctx, a.C.IstioNamespace, "configmap", a.C.CNIConfigMap)
	if e != nil {
		return fmt.Errorf("cannot verify Istio CNI configuration: %w", e)
	}
	excluded, recognized := CNIExcluded(cm, a.C.Namespace)
	if excluded {
		return fmt.Errorf("Istio CNI excludes %s: ask the mesh administrator to update existing CNI configuration; devctl will not change Istio", a.C.Namespace)
	}
	if !recognized {
		return fmt.Errorf("unrecognized CNI configuration: provide the actual CNI ConfigMap; cannot prove ambient enrollment is allowed")
	}
	ds, e := a.get(ctx, a.C.IstioNamespace, "daemonset", a.C.CNIDaemonSet)
	if e != nil {
		return e
	}
	ambient := false
	for _, v := range arr(at(ds, "spec", "template", "spec", "containers")) {
		for _, x := range arr(obj(v)["env"]) {
			ev := obj(x)
			if str(ev["name"]) == "AMBIENT_ENABLED" && str(ev["value"]) == "true" {
				ambient = true
			}
		}
	}
	if !ambient {
		return fmt.Errorf("CNI AMBIENT_ENABLED=true was not found")
	}
	nodes, e := a.get(ctx, "", "nodes", "")
	if e != nil {
		return e
	}
	actual := map[string]map[string]any{}
	for _, v := range arr(nodes["items"]) {
		n := obj(v)
		actual[str(at(n, "metadata", "name"))] = n
	}
	for i, n := range a.C.Nodes {
		m, ok := actual[n.Name]
		if !ok || str(at(m, "status", "nodeInfo", "architecture")) != n.Arch {
			return fmt.Errorf("node %s missing or architecture mismatch", n.Name)
		}
		if at(m, "spec", "unschedulable") == true {
			return fmt.Errorf("node %s is cordoned", n.Name)
		}
		if a.C.ImageMode == "nodes" {
			resolved, e := a.runtime(ctx, n, str(at(m, "status", "nodeInfo", "containerRuntimeVersion")))
			if e != nil {
				return e
			}
			a.C.Nodes[i] = resolved
		}
	}
	for _, n := range a.C.Nodes {
		for _, imName := range []string{"chisel", "tel2", "busybox", "curl"} {
			if _, e = a.image(imName, n.Arch); e != nil {
				return e
			}
		}
	}
	if _, e = a.get(ctx, a.C.Namespace, "deployment", a.C.BusinessDeployment); e != nil {
		return e
	}
	for _, d := range a.C.Dependencies {
		svc, e := a.get(ctx, d.Namespace, "service", d.Service)
		if e != nil {
			return e
		}
		found := false
		for _, p := range arr(at(svc, "spec", "ports")) {
			if obj(p)["port"] == float64(d.Port) {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("dependency %s service does not expose port %d", d.Name, d.Port)
		}
		if len(d.Selector) == 0 && len(obj(at(svc, "spec", "selector"))) == 0 {
			return fmt.Errorf("dependency %s has no pod selector; configure explicit extraEgress and selector-backed dependency", d.Name)
		}
	}
	for _, kind := range []string{"deployments.apps", "services", "secrets", "roles.rbac.authorization.k8s.io", "rolebindings.rbac.authorization.k8s.io", "networkpolicies.networking.k8s.io"} {
		for _, verb := range []string{"get", "create", "patch", "delete"} {
			b, e := a.kube(ctx, "auth", "can-i", verb, kind, "-n", a.C.Namespace)
			if e != nil || strings.TrimSpace(string(b)) != "yes" {
				return fmt.Errorf("administrator lacks %s %s in %s", verb, kind, a.C.Namespace)
			}
		}
	}
	if has(a.C.Backends, "telepresence") {
		if _, e = a.managerState(ctx); e != nil {
			return e
		}
		if a.C.ReuseManager {
			if e = a.verifyManagerScope(ctx); e != nil {
				return e
			}
		}
	}
	return nil
}
func (a *Admin) locked(p string) bool {
	for _, f := range a.L.Files {
		if f.Path == p {
			return true
		}
	}
	return false
}
func CNIExcluded(cm map[string]any, ns string) (bool, bool) {
	found := false
	excluded := false
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, v := range x {
				normalized := strings.ToLower(strings.ReplaceAll(k, "_", ""))
				if normalized == "excludenamespaces" {
					found = true
					for _, n := range arr(v) {
						if n == ns {
							excluded = true
						}
					}
				}
				visit(v)
			}
		case []any:
			for _, v := range x {
				visit(v)
			}
		}
	}
	for _, v := range obj(cm["data"]) {
		var parsed any
		if yaml.Unmarshal([]byte(str(v)), &parsed) == nil {
			visit(parsed)
		}
	}
	return excluded, found
}
func (a *Admin) image(name, arch string) (Image, error) {
	for _, im := range a.L.Images {
		if im.Name == name && im.Platform == "linux/"+arch {
			return im, nil
		}
	}
	return Image{}, fmt.Errorf("image %s for %s missing", name, arch)
}
func (a *Admin) runtime(ctx context.Context, n Node, reported string) (Node, error) {
	b, e := runSSH(ctx, a.R, n.SSH, []string{"sh", "-c", "cat /var/lib/kubelet/instance-config.yaml 2>/dev/null; ps -eo args | sed -n '/[k]ubelet/p'"}, nil)
	if e != nil {
		return n, e
	}
	text := string(b)
	endpoint := regexp.MustCompile(`(?:containerRuntimeEndpoint:|--container-runtime-endpoint[= ])\s*["']?([^\s"']+)`).FindStringSubmatch(text)
	if n.Socket == "" && len(endpoint) > 1 {
		n.Socket = endpoint[1]
	}
	n.Socket = strings.TrimPrefix(n.Socket, "unix://")
	detected := ""
	if strings.Contains(n.Socket, "containerd") {
		detected = "containerd"
	} else if strings.Contains(n.Socket, "cri-dockerd") || strings.Contains(n.Socket, "cri-docker") {
		detected = "docker"
	}
	if n.Runtime == "" || n.Runtime == "auto" {
		n.Runtime = detected
	}
	if n.Runtime == "" || n.Socket == "" {
		return n, fmt.Errorf("node %s runtime cannot be proven (kubelet reports %s); set runtime and actual CRI socket in JSON", n.Name, reported)
	}
	if detected != "" && detected != n.Runtime {
		return n, fmt.Errorf("node %s runtime/socket mismatch", n.Name)
	}
	if !strings.HasPrefix(n.Socket, "/") {
		return n, fmt.Errorf("runtime socket must be an absolute path")
	}
	if n.Ctr == "" {
		n.Ctr = "ctr"
	}
	if n.Runtime == "containerd" {
		_, e = runSSH(ctx, a.R, n.SSH, []string{n.Ctr, "--address", n.Socket, "--namespace", "k8s.io", "version"}, nil)
	} else {
		_, e = runSSH(ctx, a.R, n.SSH, []string{"docker", "version", "--format", "{{.Server.Version}}"}, nil)
	}
	if e != nil {
		return n, fmt.Errorf("node %s runtime probe failed: %w", n.Name, e)
	}
	return n, nil
}

type Revision struct {
	Name     string `json:"name"`
	Revision int    `json:"revision"`
	Chart    string `json:"chart"`
	Status   string `json:"status"`
}

func (a *Admin) release(ctx context.Context, name string) (Revision, error) {
	b, e := a.helm(ctx, "list", "--all", "-n", a.C.Namespace, "--filter", "^"+regexp.QuoteMeta(name)+"$", "-o", "json")
	if e != nil {
		return Revision{}, e
	}
	var raw []struct {
		Name     string `json:"name"`
		Revision string `json:"revision"`
		Chart    string `json:"chart"`
		Status   string `json:"status"`
	}
	if e = json.Unmarshal(b, &raw); e != nil {
		return Revision{}, e
	}
	if len(raw) == 0 {
		return Revision{}, nil
	}
	rev, _ := strconv.Atoi(raw[0].Revision)
	return Revision{raw[0].Name, rev, raw[0].Chart, raw[0].Status}, nil
}
func (a *Admin) managerState(ctx context.Context) (Revision, error) {
	r, e := a.release(ctx, a.C.ManagerRelease)
	if e != nil {
		return r, e
	}
	if r.Name != "" {
		if r.Chart != "telepresence-oss-2.31.0" || r.Status != "deployed" {
			return r, fmt.Errorf("existing manager must be deployed Telepresence 2.31.0; no implicit takeover/upgrade")
		}
		if !a.C.ReuseManager {
			b, e := a.helm(ctx, "get", "values", a.C.ManagerRelease, "-n", a.C.Namespace, "-o", "json")
			if e != nil {
				return r, e
			}
			var v map[string]any
			_ = json.Unmarshal(b, &v)
			if at(v, "labels", "devctl.io/managed") != "true" {
				return r, fmt.Errorf("existing manager is not devctl-owned; set reuseManager after reviewing scope")
			}
		}
	}
	if a.C.ReuseManager && r.Name == "" {
		return r, fmt.Errorf("reuseManager requested but release absent")
	}
	return r, nil
}
func (a *Admin) ImageRefs() map[string]string {
	refs := map[string]string{}
	arch := a.C.Nodes[0].Arch
	for _, im := range a.L.Images {
		if im.Platform != "linux/"+arch {
			continue
		}
		if a.C.ImageMode == "registry" {
			refs[im.Name] = strings.TrimSuffix(a.C.Registry.Prefix, "/") + "/" + im.Name + "@" + im.Digest
		} else {
			refs[im.Name] = im.Ref
		}
	}
	return refs
}
func (a *Admin) Values(ctx context.Context) (map[string]any, map[string]any, error) {
	// Each installation is architecture homogeneous; mixed clusters use independent releases.
	arch := a.C.Nodes[0].Arch
	if a.C.Tolerations == nil {
		a.C.Tolerations = []map[string]any{}
	}
	for _, n := range a.C.Nodes {
		if n.Arch != arch {
			return nil, nil, fmt.Errorf("one release uses one architecture; configure homogeneous eligible nodes")
		}
	}
	refs := a.ImageRefs()
	names := []string{}
	for _, n := range a.C.Nodes {
		names = append(names, n.Name)
	}
	sort.Strings(names)
	affinity := map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": names}}}}}}}
	policy := "IfNotPresent"
	if a.C.ImageMode == "nodes" {
		policy = "Never"
	}
	egress := []map[string]any{{"to": []any{map[string]any{"ipBlock": map[string]any{"cidr": a.C.ClusterDNS + "/32"}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 53}, map[string]any{"protocol": "UDP", "port": 53}}}, {"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": "kube-system"}}, "podSelector": map[string]any{"matchLabels": map[string]string{"k8s-app": "kube-dns"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 53}, map[string]any{"protocol": "UDP", "port": 53}}}}
	for _, d := range a.C.Dependencies {
		svc, e := a.get(ctx, d.Namespace, "service", d.Service)
		if e != nil {
			return nil, nil, e
		}
		sel := d.Selector
		if len(sel) == 0 {
			sel = map[string]string{}
			for k, v := range obj(at(svc, "spec", "selector")) {
				sel[k] = str(v)
			}
		}
		ports := []any{}
		for _, v := range arr(at(svc, "spec", "ports")) {
			p := obj(v)
			if p["port"] == float64(d.Port) {
				target := p["targetPort"]
				if target == nil {
					target = d.Port
				}
				ports = append(ports, map[string]any{"protocol": "TCP", "port": target})
			}
		}
		egress = append(egress, map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]string{"kubernetes.io/metadata.name": d.Namespace}}, "podSelector": map[string]any{"matchLabels": sel}}}, "ports": ports})
	}
	if has(a.C.Backends, "telepresence") {
		egress = append(egress, map[string]any{"to": []any{map[string]any{"podSelector": map[string]any{"matchLabels": map[string]string{"app": "traffic-manager", "telepresence": "manager"}}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8081}}})
	}
	egress = append(egress, a.C.ExtraEgress...)
	pulls := []any{}
	if a.C.Registry.PullSecret != "" {
		pulls = append(pulls, map[string]string{"name": a.C.Registry.PullSecret})
	}
	gateway := map[string]any{"name": a.C.GatewayRelease, "image": refs["chisel"], "imagePullPolicy": policy, "imagePullSecrets": pulls, "affinity": affinity, "tolerations": a.C.Tolerations, "ambient": true, "authSecret": a.C.Credentials.Secret, "telepresence": map[string]any{"enabled": has(a.C.Backends, "telepresence")}, "developerGroups": a.C.DeveloperGroups, "readAccess": map[string]any{"namespace": a.C.Namespace, "deployments": []string{a.C.BusinessDeployment}, "configMaps": a.C.ConfigMaps, "secrets": a.C.Secrets}, "egress": egress}
	// The pinned chart composes registry/name:tag; a tag@digest retains client-version compatibility.
	imageValues := func(ref, version string) map[string]any {
		repo := imageRepository(ref)
		parts := strings.SplitN(repo, "/", 2)
		tag := ref[strings.LastIndex(ref, ":")+1:]
		if strings.Contains(ref, "@") {
			tag = version + "@" + strings.Split(ref, "@")[1]
		}
		return map[string]any{"registry": parts[0], "name": parts[1], "tag": tag, "pullPolicy": policy}
	}
	hookValues := func(ref, version string) map[string]any {
		v := imageValues(ref, version)
		v["image"] = v["name"]
		delete(v, "name")
		v["imagePullSecrets"] = pulls
		return v
	}
	manager := map[string]any{"namespaces": []string{a.C.Namespace}, "labels": map[string]string{"devctl.io/managed": "true"}, "image": imageValues(refs["tel2"], "2.31.0"), "affinity": affinity, "tolerations": a.C.Tolerations, "agent": map[string]any{"image": imageValues(refs["tel2"], "2.31.0"), "mountPolicies": map[string]string{"credentials": "Ignore", "/credentials": "Ignore"}}, "agentInjector": map[string]any{"webhook": map[string]any{"objectSelector": map[string]any{"matchLabels": map[string]string{"devctl.io/shared-egress": "true"}}}}, "hooks": map[string]any{"busybox": hookValues(refs["busybox"], "1.36.1"), "curl": hookValues(refs["curl"], "8.1.1")}, "usage": map[string]any{"enabled": false, "collectorAddress": ""}, "client": map[string]any{"usage": map[string]any{"enabled": false}, "intercept": map[string]any{"localShortcut": false}}, "intercept": map[string]any{"allowGlobalIntercepts": false}, "nodeAgent": map[string]bool{"enabled": false}, "routeController": map[string]bool{"enabled": false}, "quicTunnel": map[string]bool{"enabled": false}}
	obj(manager["image"])["imagePullSecrets"] = pulls
	obj(at(manager, "agent", "image"))["pullSecrets"] = pulls
	return gateway, manager, nil
}
func decodeYAML(b []byte) ([]map[string]any, error) {
	d := yaml.NewDecoder(bytes.NewReader(b))
	out := []map[string]any{}
	for {
		var m map[string]any
		e := d.Decode(&m)
		if e == io.EOF {
			return out, nil
		}
		if e != nil {
			return nil, e
		}
		if len(m) > 0 {
			out = append(out, m)
		}
	}
}
func auditManifests(b []byte, refs map[string]string, policy string) error {
	docs, e := decodeYAML(b)
	if e != nil {
		return e
	}
	var walk func(any) error
	walk = func(v any) error {
		switch x := v.(type) {
		case map[string]any:
			if im, ok := x["image"].(string); ok {
				good := false
				for _, ref := range refs {
					if im == ref || (strings.Contains(ref, "@") && imageRepository(im) == imageRepository(ref) && strings.HasSuffix(im, "@"+strings.Split(ref, "@")[1])) {
						good = true
					}
				}
				if !good {
					return fmt.Errorf("unlocked image in rendered chart: %s", im)
				}
				if x["imagePullPolicy"] != policy {
					return fmt.Errorf("unexpected pull policy for %s", im)
				}
			}
			for _, v := range x {
				if e := walk(v); e != nil {
					return e
				}
			}
		case []any:
			for _, v := range x {
				if e := walk(v); e != nil {
					return e
				}
			}
		}
		return nil
	}
	for _, d := range docs {
		if d["kind"] == "Namespace" {
			return fmt.Errorf("Namespace creation forbidden")
		}
		if e := walk(d); e != nil {
			return e
		}
	}
	return nil
}
func (a *Admin) Plan(ctx context.Context) (map[string]any, error) {
	if e := a.Preflight(ctx); e != nil {
		return nil, e
	}
	g, m, e := a.Values(ctx)
	if e != nil {
		return nil, e
	}
	temp, e := os.MkdirTemp("", "devctl-plan-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(temp)
	original := a.C.WorkDir
	a.C.WorkDir = temp
	defer func() { a.C.WorkDir = original }()
	nodes := []string{}
	for _, n := range a.C.Nodes {
		nodes = append(nodes, n.Name)
	}
	policy := "IfNotPresent"
	if a.C.ImageMode == "nodes" {
		policy = "Never"
	}
	if e = writeJSON(filepath.Join(temp, "renderer.json"), RenderConfig{Refs: a.ImageRefs(), Policy: policy, Nodes: nodes, Tolerations: a.C.Tolerations}); e != nil {
		return nil, e
	}
	if _, e = a.render(ctx, a.C.GatewayRelease, filepath.Join(a.C.Bundle, "deploy", "gateway"), g); e != nil {
		return nil, e
	}
	if has(a.C.Backends, "telepresence") {
		if _, e = a.render(ctx, a.C.ManagerRelease, filepath.Join(a.C.Bundle, "charts", "telepresence.tgz"), m); e != nil {
			return nil, e
		}
	}
	return map[string]any{"ok": true, "namespace": a.C.Namespace, "imageMode": a.C.ImageMode, "nodes": a.C.Nodes, "gatewayValues": g, "managerValues": m, "steps": []string{"verify bundle and ambient", "distribute images", "ensure credentials", "install manager", "install gateway", "verify and export"}}, nil
}
func address(host string, port int) string { return net.JoinHostPort(host, strconv.Itoa(port)) }
