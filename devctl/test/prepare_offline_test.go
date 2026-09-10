package test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPrepareOfflinePowerShell(t *testing.T) {
	pwsh := os.Getenv("DEVCTL_PWSH_VALIDATOR")
	if pwsh == "" {
		pwsh, _ = exec.LookPath("pwsh")
	}
	if pwsh == "" && runtime.GOOS == "windows" {
		pwsh, _ = exec.LookPath("powershell.exe")
	}
	if pwsh == "" {
		t.Skip("set DEVCTL_PWSH_VALIDATOR to run the PowerShell wrapper tests")
	}
	root, _ := filepath.Abs("..")
	dir := t.TempDir()
	// Include spaces to exercise native argument passing and kit lookup.
	kit := filepath.Join(dir, "admin kit")
	if err := os.MkdirAll(filepath.Join(kit, "scripts"), 0700); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(kit, "scripts", "prepare-offline.ps1")
	b, err := os.ReadFile(filepath.Join(root, "scripts", "prepare-offline.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(script, b, 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(kit, "devctl.exe")
	build := exec.Command("go", "build", "-o", binary, "./test/fixture")
	build.Dir = root
	build.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if b, err = build.CombinedOutput(); err != nil {
		t.Fatalf("fixture build: %v %s", err, b)
	}
	fixture, _ := os.ReadFile(binary)
	explicit := filepath.Join(dir, "explicit-devctl.exe")
	if err = os.WriteFile(explicit, fixture, 0700); err != nil {
		t.Fatal(err)
	}
	staleDir := filepath.Join(dir, "old-path")
	os.MkdirAll(staleDir, 0700)
	if err = os.WriteFile(filepath.Join(staleDir, "devctl.exe"), fixture, 0700); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	for _, tc := range []struct {
		name                  string
		env                   map[string]string
		extra                 string
		wantProxy, wantBypass string
		code                  int
		explicit              bool
	}{
		{name: "window-environment", env: map[string]string{"HTTP_PROXY": "http://127.0.0.1:18080", "HTTPS_PROXY": "http://proxy-user:proxy-password@127.0.0.1:18081", "NO_PROXY": "localhost,.internal"}, wantProxy: "http://proxy-user:proxy-password@127.0.0.1:18081", wantBypass: "localhost,.internal"},
		{name: "http-only-and-bare-address", env: map[string]string{"HTTP_PROXY": " 127.0.0.1:18080 "}, wantProxy: "http://127.0.0.1:18080"},
		{name: "lowercase", env: map[string]string{"https_proxy": "http://127.0.0.1:18081", "no_proxy": ".internal"}, wantProxy: "http://127.0.0.1:18081", wantBypass: ".internal"},
		{name: "explicit-and-clear-bypass", env: map[string]string{"HTTPS_PROXY": "http://old.example:80", "NO_PROXY": "*"}, extra: " -Proxy '127.0.0.1:18082' -NoProxy ''", wantProxy: "http://127.0.0.1:18082", explicit: true},
		{name: "child-failure-restores-env", env: map[string]string{"HTTP_PROXY": "127.0.0.1:18080", "DEVCTL_TEST_EXIT": "7"}, wantProxy: "http://127.0.0.1:18080", code: 7},
		{name: "no-proxy", env: map[string]string{}},
		{name: "invalid-proxy-redacted", env: map[string]string{"HTTPS_PROXY": "ftp://proxy-user:proxy-password@127.0.0.1:80"}, code: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := tc.extra
			wantExe := binary
			if tc.explicit {
				extra += " -Devctl " + quote(explicit)
				wantExe = explicit
			}
			// Invoke as a nested script, then check the caller's environment on both
			// success and failure. Diagnostics must stay off JSON stdout.
			command := `$ErrorActionPreference='Stop'
$PSNativeCommandUseErrorActionPreference=$true
$names=@('HTTP_PROXY','HTTPS_PROXY','NO_PROXY','http_proxy','https_proxy','no_proxy')
$before=@($names | ForEach-Object { [Environment]::GetEnvironmentVariable($_,'Process') })
$code=0
try { & ` + quote(script) + ` -Config 'config with spaces.json'` + extra + `; $code=$LASTEXITCODE } catch { [Console]::Error.WriteLine($_.Exception.Message); $code=9 }
$after=@($names | ForEach-Object { [Environment]::GetEnvironmentVariable($_,'Process') })
if (($before | ConvertTo-Json -Compress) -cne ($after | ConvertTo-Json -Compress)) { [Console]::Error.WriteLine('environment was not restored'); exit 98 }
exit $code`
			cmd := exec.Command(pwsh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", command)
			for _, e := range os.Environ() {
				key, _, _ := strings.Cut(e, "=")
				key = strings.ToUpper(key)
				if strings.HasSuffix(key, "_PROXY") || strings.HasPrefix(key, "DEVCTL_TEST_") || key == "PATH" {
					continue
				}
				cmd.Env = append(cmd.Env, e)
			}
			cmd.Env = append(cmd.Env, "PATH="+staleDir+string(os.PathListSeparator)+os.Getenv("PATH"), "DEVCTL_TEST_PREPARE_FIXTURE=1")
			for key, val := range tc.env {
				cmd.Env = append(cmd.Env, key+"="+val)
			}
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				if e, ok := err.(*exec.ExitError); ok {
					code = e.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tc.code {
				t.Fatalf("exit %d expected %d: %s", code, tc.code, stderr.String())
			}
			if strings.Contains(stderr.String(), "proxy-password") || strings.Contains(stderr.String(), "proxy-user") {
				t.Fatal("proxy credentials in diagnostic output")
			}
			if tc.code == 9 {
				if out.Len() != 0 {
					t.Fatal("invalid proxy launched downloader")
				}
				return
			}
			var got struct {
				Args []string
				Env  map[string]string
				Exe  string
			}
			if err = json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatalf("stdout is not JSON: %v %s", err, out.String())
			}
			if !reflect.DeepEqual(got.Args, []string{"bundle", "prepare", "--config", "config with spaces.json", "--format", "json"}) {
				t.Fatal(got.Args)
			}
			if got.Env["HTTPS_PROXY"] != tc.wantProxy || got.Env["https_proxy"] != tc.wantProxy || got.Env["NO_PROXY"] != tc.wantBypass || got.Env["no_proxy"] != tc.wantBypass {
				t.Fatal("proxy environment mismatch")
			}
			if got.Exe != wantExe {
				t.Fatalf("wrong executable: %s expected %s", got.Exe, wantExe)
			}
			if !strings.Contains(stderr.String(), "prepare-offline:") {
				t.Fatal("missing diagnostics")
			}
			if tc.name == "no-proxy" && !strings.Contains(stderr.String(), "$env:HTTPS_PROXY") {
				t.Fatal("missing PowerShell environment syntax hint")
			}
		})
	}
}
