package cmd

import (
	"testing"

	"github.com/hzeng10/local-debug/internal/tp"
)

// An existing session scoped to another namespace must be replaced, not reused:
// the intercept and down's uninstall resolve in the CONNECTED namespace (this is
// exactly how a manual `telepresence connect` without -n stranded a kube-system
// intercept). But never tear down a session that carries live intercepts.
func TestSessionAction(t *testing.T) {
	if r, err := sessionAction(&tp.Status{Namespace: "kube-system"}, "kube-system"); r || err != nil {
		t.Errorf("matching namespace must reuse the session: %v %v", r, err)
	}
	// Older daemons may not report a namespace — do not churn the session.
	if r, err := sessionAction(&tp.Status{}, "kube-system"); r || err != nil {
		t.Errorf("unknown session namespace must reuse: %v %v", r, err)
	}
	if r, err := sessionAction(&tp.Status{Namespace: "default"}, "kube-system"); !r || err != nil {
		t.Errorf("idle mismatched session must reconnect: %v %v", r, err)
	}
	if _, err := sessionAction(&tp.Status{
		Namespace: "default", Intercepts: []tp.InterceptInfo{{Name: "x"}},
	}, "kube-system"); err == nil {
		t.Error("a session with live intercepts must be refused, not quit")
	}
}
