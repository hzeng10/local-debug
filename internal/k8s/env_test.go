package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveEnv(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "app-config", Namespace: "demo"},
		Data:       map[string]string{"LOG_LEVEL": "debug", "REGION": "eu"},
	}
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-creds", Namespace: "demo"},
		Data:       map[string][]byte{"PASSWORD": []byte("s3cr3t")},
	}
	cs := fake.NewSimpleClientset(cm, sec)
	c := NewClientFromInterface(cs, "demo")

	ctr := corev1.Container{
		Name: "orders",
		EnvFrom: []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "app-config"}}},
		},
		Env: []corev1.EnvVar{
			{Name: "SPRING_PROFILES_ACTIVE", Value: "cluster"},
			// override an envFrom value via explicit env
			{Name: "LOG_LEVEL", Value: "info"},
			{Name: "DB_PASSWORD", ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db-creds"}, Key: "PASSWORD"},
			}},
			{Name: "POD_IP", ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
			}},
		},
	}

	vars, err := c.ResolveEnv(context.Background(), "demo", ctr)
	if err != nil {
		t.Fatalf("ResolveEnv: %v", err)
	}

	got := map[string]EnvVar{}
	for _, v := range vars {
		got[v.Name] = v
	}

	if v := got["LOG_LEVEL"]; v.Value != "info" {
		t.Errorf("LOG_LEVEL: env should override envFrom; got %q (source %q)", v.Value, v.Source)
	}
	if v := got["REGION"]; v.Value != "eu" {
		t.Errorf("REGION from envFrom configMap: got %q", v.Value)
	}
	if v := got["DB_PASSWORD"]; v.Value != "s3cr3t" || !v.Secret {
		t.Errorf("DB_PASSWORD: want secret s3cr3t, got %q secret=%v", v.Value, v.Secret)
	}
	if v := got["POD_IP"]; !v.Skipped {
		t.Errorf("POD_IP fieldRef should be Skipped, got %+v", v)
	}
	// dedupe must keep exactly one LOG_LEVEL
	n := 0
	for _, v := range vars {
		if v.Name == "LOG_LEVEL" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("LOG_LEVEL appears %d times, want 1 (dedupeKeepLast)", n)
	}
}
