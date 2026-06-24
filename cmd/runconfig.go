package cmd

import (
	"strings"

	"github.com/hzeng10/local-debug/internal/springconfig"
)

// emitRunConfig generates an IDE run config in the cwd, wired to load the env-file.
// ide is "intellij" or "vscode". Failures are surfaced via Info (non-fatal).
func emitRunConfig(ide, service, envFile string) {
	switch strings.ToLower(ide) {
	case "intellij", "idea":
		path, note, err := springconfig.WriteIntelliJRunConfig(".", service, envFile, "")
		if err != nil {
			out.Info("! run-config (intellij) failed: %v", err)
			return
		}
		out.Info("✓ IntelliJ run config → %s (%s)", path, note)
	case "vscode", "code":
		path, snippet, err := springconfig.WriteVSCodeLaunch(".", service, envFile)
		if err != nil {
			out.Info("! run-config (vscode) failed: %v", err)
			return
		}
		if snippet {
			out.Info("✓ VS Code launch snippet → %s (merge into your .vscode/launch.json)", path)
		} else {
			out.Info("✓ VS Code launch config → %s", path)
		}
	default:
		out.Info("! unknown --run-config %q (use intellij|vscode)", ide)
	}
}
