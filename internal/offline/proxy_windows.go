//go:build windows

package offline

import "golang.org/x/sys/windows/registry"

// systemProxy reads the per-user Internet Settings that Windows proxy-mode VPN
// clients (Clash, v2rayN, …) configure. Go's own ProxyFromEnvironment never looks
// here, which is why `ldbg bundle` could time out while the browser worked.
func systemProxy() (sysProxy, bool) {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return sysProxy{}, false
	}
	defer k.Close()

	pac, _, _ := k.GetStringValue("AutoConfigURL")
	enable, _, err := k.GetIntegerValue("ProxyEnable")
	server, _, _ := k.GetStringValue("ProxyServer")
	if err != nil || enable == 0 || server == "" {
		// A PAC-only configuration is still worth reporting: ldbg cannot evaluate
		// it, and saying so beats silently connecting direct.
		if pac != "" {
			return sysProxy{PAC: pac}, true
		}
		return sysProxy{}, false
	}
	override, _, _ := k.GetStringValue("ProxyOverride")
	httpProxy, httpsProxy := parseWindowsProxyServer(server)
	return sysProxy{HTTP: httpProxy, HTTPS: httpsProxy, Bypass: override, PAC: pac}, true
}
