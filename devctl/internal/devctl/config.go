package devctl

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

type ObjectRef struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
}
type KeyRef struct {
	ObjectRef
	Key string `json:"key"`
}
type EnvVar struct {
	Name      string `json:"name"`
	Value     string `json:"value"`
	ValueFrom *struct {
		SecretKeyRef    *KeyRef `json:"secretKeyRef"`
		ConfigMapKeyRef *KeyRef `json:"configMapKeyRef"`
		FieldRef        *struct {
			FieldPath string `json:"fieldPath"`
		} `json:"fieldRef"`
		ResourceFieldRef any `json:"resourceFieldRef"`
	} `json:"valueFrom"`
}
type Container struct {
	Name    string   `json:"name"`
	Env     []EnvVar `json:"env"`
	EnvFrom []struct {
		Prefix       string     `json:"prefix"`
		ConfigMapRef *ObjectRef `json:"configMapRef"`
		SecretRef    *ObjectRef `json:"secretRef"`
	} `json:"envFrom"`
}
type Deployment struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []Container `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}
type DataObject struct {
	Type       string            `json:"type"`
	Data       map[string]string `json:"data"`
	BinaryData map[string]string `json:"binaryData"`
}
type Prepared struct {
	Profile Profile
	Env     map[string]string
	Secrets []string
	Dir     string
}

var kubeExpansion = regexp.MustCompile(`\$\$|\$\(([A-Za-z_][A-Za-z0-9_]*)\)`)

func expandKube(s string, env map[string]string) string {
	return kubeExpansion.ReplaceAllStringFunc(s, func(x string) string {
		if x == "$$" {
			return "$"
		}
		if v, ok := env[x[2:len(x)-1]]; ok {
			return v
		}
		return x
	})
}

func Prepare(ctx context.Context, p Profile, r Reader, dir string) (Prepared, error) {
	result := Prepared{Profile: p, Env: map[string]string{}, Dir: dir}
	var dep Deployment
	if err := r.Get(ctx, p.Cluster.Namespace, "deployment", p.Source.Deployment, &dep); err != nil {
		return result, err
	}
	var c *Container
	for i := range dep.Spec.Template.Spec.Containers {
		if dep.Spec.Template.Spec.Containers[i].Name == p.Source.Container {
			c = &dep.Spec.Template.Spec.Containers[i]
		}
	}
	if c == nil {
		return result, fmt.Errorf("container %q is absent in source deployment", p.Source.Container)
	}
	cache := map[string]map[string]string{}
	read := func(kind, name string) (map[string]string, error) {
		key := kind + "/" + name
		if m, ok := cache[key]; ok {
			return m, nil
		}
		var obj DataObject
		if err := r.Get(ctx, p.Cluster.Namespace, kind, name, &obj); err != nil {
			return nil, err
		}
		if obj.Type == "kubernetes.io/service-account-token" {
			return nil, fmt.Errorf("service-account tokens must remain in the cluster")
		}
		m := map[string]string{}
		for k, v := range obj.Data {
			if kind == "secret" {
				b, e := base64.StdEncoding.DecodeString(v)
				if e != nil {
					return nil, fmt.Errorf("invalid base64 in secret %s", name)
				}
				v = string(b)
				if v != "" {
					result.Secrets = append(result.Secrets, v)
				}
			}
			m[k] = v
		}
		for k, v := range obj.BinaryData {
			b, e := base64.StdEncoding.DecodeString(v)
			if e != nil {
				return nil, fmt.Errorf("invalid binaryData in %s", name)
			}
			m[k] = string(b)
		}
		cache[key] = m
		return m, nil
	}
	// Resolve the full environment in Kubernetes order, then export only the allowlist.
	// Failed reads (including optional references) fail closed rather than masking RBAC/network errors.
	all := map[string]string{}
	unresolved := map[string]bool{}
	for _, ref := range c.EnvFrom {
		kind := "configmap"
		o := ref.ConfigMapRef
		if ref.SecretRef != nil {
			kind = "secret"
			o = ref.SecretRef
		}
		if o == nil {
			return result, fmt.Errorf("invalid envFrom")
		}
		m, e := read(kind, o.Name)
		if e != nil {
			return result, e
		}
		for k, v := range m {
			all[ref.Prefix+k] = v
		}
	}
	for _, e := range c.Env {
		if e.ValueFrom == nil {
			all[e.Name] = expandKube(e.Value, all)
			continue
		}
		v := e.ValueFrom
		switch {
		case v.ConfigMapKeyRef != nil || v.SecretKeyRef != nil:
			kind := "configmap"
			ref := v.ConfigMapKeyRef
			if v.SecretKeyRef != nil {
				kind = "secret"
				ref = v.SecretKeyRef
			}
			m, err := read(kind, ref.Name)
			if err != nil {
				return result, err
			}
			value, ok := m[ref.Key]
			if !ok {
				if ref.Optional {
					continue
				}
				return result, fmt.Errorf("missing key %s in %s/%s", ref.Key, kind, ref.Name)
			}
			all[e.Name] = value
		case v.FieldRef != nil && v.FieldRef.FieldPath == "metadata.namespace":
			all[e.Name] = p.Cluster.Namespace
		default:
			unresolved[e.Name] = true
		}
	}
	for _, key := range p.Source.EnvAllow {
		if _, override := p.Source.Overrides[key]; override {
			continue
		}
		if unresolved[key] {
			return result, fmt.Errorf("%s is Pod-specific; supply an explicit local override", key)
		}
		v, ok := all[key]
		if !ok {
			return result, fmt.Errorf("allowlisted variable %s not found; declare image/entrypoint values as overrides", key)
		}
		if strings.Contains(v, "$(") {
			return result, fmt.Errorf("unresolved Kubernetes expansion in %s", key)
		}
		result.Env[key] = v
	}
	if err := secureDir(dir); err != nil {
		return result, err
	}
	for _, f := range p.Source.Files {
		m, e := read(f.Kind, f.Name)
		if e != nil {
			return result, e
		}
		v, ok := m[f.Key]
		if !ok {
			return result, fmt.Errorf("missing file key %s", f.Key)
		}
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := secureDir(filepath.Dir(path)); err != nil {
			return result, err
		}
		if err := writePrivate(path, []byte(v)); err != nil {
			return result, err
		}
	}
	for k, v := range p.Source.Overrides {
		v = strings.ReplaceAll(v, "${CONFIG_DIR}", filepath.ToSlash(dir))
		v = strings.ReplaceAll(v, "${NAMESPACE}", p.Cluster.Namespace)
		v = strings.ReplaceAll(v, "${CLUSTER_DOMAIN}", p.Cluster.Domain)
		if strings.Contains(v, "${") {
			return result, fmt.Errorf("unknown override placeholder in %s", k)
		}
		result.Env[k] = v
	}
	for k, v := range result.Env {
		if strings.ContainsRune(v, 0) {
			return result, fmt.Errorf("NUL in environment variable %s", k)
		}
	}
	for _, k := range p.Source.RequiredEnv {
		if result.Env[k] == "" {
			return result, fmt.Errorf("required environment variable %s is empty", k)
		}
	}
	// Redact all exported values as well as source Secrets: composed credentials are sensitive too.
	for _, v := range result.Env {
		if v != "" {
			result.Secrets = append(result.Secrets, v)
		}
	}
	return result, nil
}

func appEnvironment(p Prepared) []string {
	// Deliberate inheritance prevents ambient SPRING_*/JAVA_TOOL_OPTIONS overriding the snapshot.
	allowed := []string{"PATH", "PATHEXT", "SystemRoot", "WINDIR", "COMSPEC", "TEMP", "TMP", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "JAVA_HOME", "MAVEN_HOME", "GRADLE_USER_HOME", "LANG", "TERM"}
	allowed = append(allowed, p.Profile.Application.InheritEnv...)
	m := map[string]string{}
	for _, k := range allowed {
		if v, ok := os.LookupEnv(k); ok {
			if runtime.GOOS == "windows" {
				k = strings.ToUpper(k)
			}
			m[k] = v
		}
	}
	for k, v := range p.Env {
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		m[k] = v
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}
