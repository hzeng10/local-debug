// Package mesh interprets Istio ambient state for an intercept target. The decisive
// fact (validated on minikube): an intercepted workload that stays in ambient gets its
// app port black-holed — istio-cni's ztunnel redirection and the telepresence
// traffic-agent both claim the port, so in-cluster callers see "connection reset".
// The fix is to exclude the *intercepted* workload from ambient (dataplane-mode=none);
// dependencies remain in the mesh.
package mesh

import "github.com/hzeng10/local-debug/internal/k8s"

// AmbientAssessment is the verdict for one workload.
type AmbientAssessment struct {
	NamespaceMode     string `json:"namespaceMode"`
	WorkloadMode      string `json:"workloadMode"`
	NamespaceAmbient  bool   `json:"namespaceAmbient"`
	WorkloadInAmbient bool   `json:"workloadInAmbient"`
	NeedsOptOut       bool   `json:"needsOptOut"`
	AlreadyOptedOut   bool   `json:"alreadyOptedOut"`
	Explanation       string `json:"explanation"`
}

// AssessWorkload computes whether the intercept target needs the ambient opt-out.
// nsMode is the namespace's istio.io/dataplane-mode; podMode is the pod template's
// override ("" = inherit namespace).
func AssessWorkload(nsMode, podMode string) AmbientAssessment {
	a := AmbientAssessment{NamespaceMode: nsMode, WorkloadMode: podMode}
	a.NamespaceAmbient = nsMode == k8s.DataplaneAmbient

	switch {
	case podMode == k8s.DataplaneNone:
		a.AlreadyOptedOut = true
		a.Explanation = "workload already excluded from ambient (dataplane-mode=none); intercept is safe"
	case podMode == k8s.DataplaneAmbient:
		a.WorkloadInAmbient = true
		a.Explanation = "workload explicitly in ambient; opt it out for a working intercept"
	case a.NamespaceAmbient:
		a.WorkloadInAmbient = true
		a.Explanation = "workload inherits ambient from its namespace; opt it out for a working intercept"
	default:
		a.Explanation = "not an ambient workload; no opt-out needed"
	}
	a.NeedsOptOut = a.WorkloadInAmbient
	return a
}
