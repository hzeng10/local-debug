package mesh

import "testing"

func TestAssessWorkload(t *testing.T) {
	cases := []struct {
		name, nsMode, podMode    string
		wantNeedsOptOut, wantOut bool
	}{
		{"ambient ns, inherited", "ambient", "", true, false},
		{"ambient ns, opted out", "ambient", "none", false, true},
		{"non-ambient ns", "", "", false, false},
		{"non-ambient ns, pod forced ambient", "", "ambient", true, false},
		{"non-ambient ns, pod none", "", "none", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := AssessWorkload(c.nsMode, c.podMode)
			if a.NeedsOptOut != c.wantNeedsOptOut {
				t.Errorf("NeedsOptOut=%v want %v (%+v)", a.NeedsOptOut, c.wantNeedsOptOut, a)
			}
			if a.AlreadyOptedOut != c.wantOut {
				t.Errorf("AlreadyOptedOut=%v want %v (%+v)", a.AlreadyOptedOut, c.wantOut, a)
			}
		})
	}
}
