//go:build !windows

package offline

// systemProxy is Windows-only: on Linux and macOS the proxy configuration tools
// and VPN clients use is already the HTTP(S)_PROXY environment, which Go honours.
func systemProxy() (sysProxy, bool) { return sysProxy{}, false }
