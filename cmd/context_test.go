package cmd

import "testing"

// The manager namespace must be the SAME value for install, connect and log
// reading: `telepresence connect --manager-namespace` overrides even the
// client's config file, so if these three ever disagree the daemon looks for a
// manager that is not there. One resolver, one precedence order.
func TestManagerNSPrecedence(t *testing.T) {
	defer func(old string) { flagManagerNamespace = old }(flagManagerNamespace)

	flagManagerNamespace = ""
	if got := managerNS(); got != DefaultManagerNamespace {
		t.Errorf("default = %q, want %q", got, DefaultManagerNamespace)
	}
	// The environment reaches the flag through its default at registration time;
	// once set, either source resolves the same way.
	flagManagerNamespace = "telepresence"
	if got := managerNS(); got != "telepresence" {
		t.Errorf("override = %q", got)
	}
	// Whitespace-only is not a namespace — fall back rather than asking the
	// cluster for "  ".
	flagManagerNamespace = "   "
	if got := managerNS(); got != DefaultManagerNamespace {
		t.Errorf("blank override = %q, want the default", got)
	}
}
