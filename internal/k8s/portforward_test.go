package k8s

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func pfService(ns, name string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.ServiceSpec{Selector: selector},
	}
}

func pfPod(ns, name string, labels map[string]string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Status: corev1.PodStatus{
			Phase:      phase,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

func TestPodForService(t *testing.T) {
	sel := map[string]string{"app.kubernetes.io/name": "victorialogs"}
	cases := []struct {
		name    string
		objs    []runtime.Object
		want    string
		wantErr string
	}{
		{
			name: "picks the ready running pod",
			objs: []runtime.Object{
				pfService("logging", "victorialogs", sel),
				pfPod("logging", "victorialogs-0", sel, corev1.PodRunning, true),
			},
			want: "victorialogs-0",
		},
		{
			name: "skips not-ready and pending pods",
			objs: []runtime.Object{
				pfService("logging", "victorialogs", sel),
				pfPod("logging", "vl-pending", sel, corev1.PodPending, false),
				pfPod("logging", "vl-notready", sel, corev1.PodRunning, false),
				pfPod("logging", "vl-good", sel, corev1.PodRunning, true),
			},
			want: "vl-good",
		},
		{
			name:    "missing service errors",
			objs:    nil,
			wantErr: "get service",
		},
		{
			name:    "no backing pods errors",
			objs:    []runtime.Object{pfService("logging", "victorialogs", sel)},
			wantErr: "no ready pod",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cl := NewClientFromInterface(fake.NewSimpleClientset(tc.objs...), "logging")
			got, err := cl.podForService(context.Background(), "logging", "victorialogs")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("podForService = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPortForwardServiceNilRestConfig(t *testing.T) {
	cl := NewClientFromInterface(fake.NewSimpleClientset(
		pfService("logging", "victorialogs", map[string]string{"a": "b"}),
		pfPod("logging", "victorialogs-0", map[string]string{"a": "b"}, corev1.PodRunning, true),
	), "logging")
	_, err := cl.PortForwardService(context.Background(), "logging", "victorialogs", 9428)
	if err == nil || !strings.Contains(err.Error(), "no rest.Config") {
		t.Fatalf("want no-rest.Config error, got %v", err)
	}
}
