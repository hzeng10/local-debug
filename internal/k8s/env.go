package k8s

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnvVar is one resolved environment variable destined for the laptop. Secret marks
// values sourced from a Secret so callers can mask them in output. Skipped marks
// values that cannot be resolved statically (pod-runtime fieldRef/resourceFieldRef).
type EnvVar struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Secret  bool   `json:"secret"`
	Source  string `json:"source"`
	Skipped bool   `json:"skipped,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ResolveEnv expands a container's env + envFrom into concrete variables, reading the
// referenced ConfigMaps/Secrets. Kubernetes precedence is honored: envFrom first,
// then env (a later same-named entry overrides an earlier one). fieldRef and
// resourceFieldRef are recorded as Skipped because they are only known at pod runtime.
func (c *Client) ResolveEnv(ctx context.Context, ns string, ctr corev1.Container) ([]EnvVar, error) {
	cmCache := map[string]*corev1.ConfigMap{}
	secCache := map[string]*corev1.Secret{}
	var ordered []EnvVar

	// envFrom: bulk-import all keys of each ConfigMap/Secret, applying any prefix.
	for _, ef := range ctr.EnvFrom {
		switch {
		case ef.ConfigMapRef != nil:
			cm, err := c.getConfigMap(ctx, ns, ef.ConfigMapRef.Name, cmCache)
			if err != nil {
				if isOptional(ef.ConfigMapRef.Optional) && apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			for k, v := range cm.Data {
				ordered = append(ordered, EnvVar{Name: ef.Prefix + k, Value: v, Source: "configMap:" + cm.Name})
			}
		case ef.SecretRef != nil:
			sec, err := c.getSecret(ctx, ns, ef.SecretRef.Name, secCache)
			if err != nil {
				if isOptional(ef.SecretRef.Optional) && apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			for k, v := range sec.Data {
				ordered = append(ordered, EnvVar{Name: ef.Prefix + k, Value: string(v), Secret: true, Source: "secret:" + sec.Name})
			}
		}
	}

	// env: literals and single-key valueFrom references (override envFrom).
	for _, e := range ctr.Env {
		if e.ValueFrom == nil {
			ordered = append(ordered, EnvVar{Name: e.Name, Value: e.Value, Source: "literal"})
			continue
		}
		vf := e.ValueFrom
		switch {
		case vf.ConfigMapKeyRef != nil:
			ref := vf.ConfigMapKeyRef
			cm, err := c.getConfigMap(ctx, ns, ref.Name, cmCache)
			if err != nil {
				if isOptional(ref.Optional) && apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			v, ok := cm.Data[ref.Key]
			if !ok {
				if isOptional(ref.Optional) {
					continue
				}
				return nil, fmt.Errorf("configMap %q has no key %q (env %q)", ref.Name, ref.Key, e.Name)
			}
			ordered = append(ordered, EnvVar{Name: e.Name, Value: v, Source: fmt.Sprintf("configMap:%s/%s", ref.Name, ref.Key)})
		case vf.SecretKeyRef != nil:
			ref := vf.SecretKeyRef
			sec, err := c.getSecret(ctx, ns, ref.Name, secCache)
			if err != nil {
				if isOptional(ref.Optional) && apierrors.IsNotFound(err) {
					continue
				}
				return nil, err
			}
			b, ok := sec.Data[ref.Key]
			if !ok {
				if isOptional(ref.Optional) {
					continue
				}
				return nil, fmt.Errorf("secret %q has no key %q (env %q)", ref.Name, ref.Key, e.Name)
			}
			ordered = append(ordered, EnvVar{Name: e.Name, Value: string(b), Secret: true, Source: fmt.Sprintf("secret:%s/%s", ref.Name, ref.Key)})
		case vf.FieldRef != nil:
			ordered = append(ordered, EnvVar{Name: e.Name, Skipped: true,
				Reason: "fieldRef " + vf.FieldRef.FieldPath + " is resolved at pod runtime"})
		case vf.ResourceFieldRef != nil:
			ordered = append(ordered, EnvVar{Name: e.Name, Skipped: true,
				Reason: "resourceFieldRef " + vf.ResourceFieldRef.Resource + " is resolved at pod runtime"})
		}
	}

	return dedupeKeepLast(ordered), nil
}

func (c *Client) getConfigMap(ctx context.Context, ns, name string, cache map[string]*corev1.ConfigMap) (*corev1.ConfigMap, error) {
	if cm, ok := cache[name]; ok {
		return cm, nil
	}
	cm, err := c.cs.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get configMap %q: %w", name, err)
	}
	cache[name] = cm
	return cm, nil
}

func (c *Client) getSecret(ctx context.Context, ns, name string, cache map[string]*corev1.Secret) (*corev1.Secret, error) {
	if s, ok := cache[name]; ok {
		return s, nil
	}
	s, err := c.cs.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get secret %q: %w", name, err)
	}
	cache[name] = s
	return s, nil
}

func isOptional(p *bool) bool { return p != nil && *p }

// dedupeKeepLast collapses duplicate names keeping the last occurrence (env over
// envFrom), preserving first-seen order of the surviving entries.
func dedupeKeepLast(in []EnvVar) []EnvVar {
	lastIdx := map[string]int{}
	for i, e := range in {
		lastIdx[e.Name] = i
	}
	var out []EnvVar
	seen := map[string]bool{}
	for i, e := range in {
		if lastIdx[e.Name] != i || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		out = append(out, e)
	}
	return out
}
