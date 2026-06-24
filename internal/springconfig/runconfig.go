package springconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DetectBuildTool returns "maven", "gradle", or "" based on files in dir.
func DetectBuildTool(dir string) string {
	if exists(filepath.Join(dir, "pom.xml")) {
		return "maven"
	}
	if exists(filepath.Join(dir, "build.gradle")) || exists(filepath.Join(dir, "build.gradle.kts")) {
		return "gradle"
	}
	return ""
}

// WriteIntelliJRunConfig writes .run/<service> (ldbg).run.xml — a Gradle/Maven run
// config that injects the synced env-file via the EnvFile plugin (net.ashald.envfile).
// IntelliJ reads each file under .run/, so this never clobbers existing configs.
// Returns the path written and a note (e.g. the EnvFile plugin requirement).
func WriteIntelliJRunConfig(workspaceDir, service, envFile, buildTool string) (string, string, error) {
	if buildTool == "" {
		buildTool = DetectBuildTool(workspaceDir)
	}
	var inner string
	switch buildTool {
	case "maven":
		inner = `    <configuration name="` + service + ` (ldbg)" type="MavenRunConfiguration" factoryName="Maven">
      <MavenSettings>
        <option name="myRunnerParameters">
          <MavenRunnerParameters>
            <option name="goals"><list><option value="spring-boot:run" /></list></option>
            <option name="workingDirPath" value="$PROJECT_DIR$" />
          </MavenRunnerParameters>
        </option>
      </MavenSettings>` + envFileExt(envFile) + `
      <method v="2" />
    </configuration>`
	default: // gradle (also the fallback)
		inner = `    <configuration name="` + service + ` (ldbg)" type="GradleRunConfiguration" factoryName="Gradle">
      <ExternalSystemSettings>
        <option name="externalProjectPath" value="$PROJECT_DIR$" />
        <option name="taskNames"><list><option value="bootRun" /></list></option>
      </ExternalSystemSettings>` + envFileExt(envFile) + `
      <method v="2" />
    </configuration>`
	}
	doc := `<component name="ProjectRunConfigurationManager">
` + inner + `
</component>
`
	dir := filepath.Join(workspaceDir, ".run")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	path := filepath.Join(dir, sanitize(service)+"-ldbg.run.xml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		return "", "", err
	}
	return path, "requires the IntelliJ EnvFile plugin (net.ashald.envfile) to load the env-file", nil
}

func envFileExt(envFile string) string {
	p := "$PROJECT_DIR$/" + filepath.ToSlash(envFile)
	return `
      <extension name="net.ashald.envfile">
        <option name="IS_ENABLED" value="true" />
        <ENTRIES>
          <ENTRY IS_ENABLED="true" PARSING_TYPE="0" PATH="` + p + `" />
        </ENTRIES>
      </extension>`
}

// WriteVSCodeLaunch creates .vscode/launch.json with a Java launch config that loads
// the env-file via the native "envFile" option (no plugin needed). To avoid clobbering
// an existing launch.json, if one is present the config is written as a snippet under
// .ldbg/ for the developer to merge. Returns (path, isSnippet, error).
func WriteVSCodeLaunch(workspaceDir, service, envFile string) (string, bool, error) {
	cfg := fmt.Sprintf(`{
      "type": "java",
      "name": %q,
      "request": "launch",
      "mainClass": "",
      "envFile": "${workspaceFolder}/%s"
    }`, service+" (ldbg)", filepath.ToSlash(envFile))

	launch := filepath.Join(workspaceDir, ".vscode", "launch.json")
	if exists(launch) {
		snippet := filepath.Join(workspaceDir, ".ldbg", "vscode-"+sanitize(service)+".launch.json")
		if err := os.MkdirAll(filepath.Dir(snippet), 0o755); err != nil {
			return "", true, err
		}
		if err := os.WriteFile(snippet, []byte(cfg+"\n"), 0o644); err != nil {
			return "", true, err
		}
		return snippet, true, nil
	}
	full := `{
  "version": "0.2.0",
  "configurations": [
` + indent(cfg, "    ") + `
  ]
}
`
	if err := os.MkdirAll(filepath.Dir(launch), 0o755); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(launch, []byte(full), 0o644); err != nil {
		return "", false, err
	}
	return launch, false, nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func sanitize(s string) string {
	return strings.NewReplacer("/", "-", " ", "_", ":", "-").Replace(s)
}

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}
