// Package locallog owns the laptop-side log file used to fill the intercept
// window: while a service is intercepted its fresh logs exist only on the
// laptop, so ldbg captures them to .ldbg/logs/<service>.log (via tee for
// `up --run`, via an injected LOGGING_FILE_NAME for IDE runs) and `ldbg logs
// local` queries that file with the same filter vocabulary as `logs query`.
package locallog

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hzeng10/local-debug/internal/k8s"
	"github.com/hzeng10/local-debug/internal/springconfig"
)

// EnvVar is the Spring Boot property (in relaxed-binding env form) that points
// the app's own file appender at the local log file: logging.file.name.
const EnvVar = "LOGGING_FILE_NAME"

// PathFor returns the absolute canonical local log path for a service:
// <cwd>/.ldbg/logs/<sanitized>.log. Absolute on purpose — a relative path in
// the env-file would silently depend on the IDE's working directory.
func PathFor(service string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return filepath.Join(cwd, ".ldbg", "logs", springconfig.Sanitize(service)+".log"), nil
}

// OpenAppend opens path for appending (creating parents), 0600 because app
// logs may echo secret-bearing startup config.
func OpenAppend(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
}

// StripVar returns env ("K=V" strings) minus entries whose key matches AND,
// when value is non-empty, whose value matches too. `up --run` uses it to drop
// ldbg's own synthetic injection (tee is the single writer there) while leaving
// a genuinely cluster-defined variable of the same name untouched.
func StripVar(env []string, key, value string) []string {
	out := env[:0]
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if k == key && (value == "" || v == value) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// InjectEnvVar appends the synthetic LOGGING_FILE_NAME variable pointing at
// PathFor(service), unless the cluster already defines a non-skipped variable
// of that name (cluster wins — ldbg never overrides real config). Returns the
// updated vars and the injected path ("" when skipped).
func InjectEnvVar(vars []k8s.EnvVar, service string) ([]k8s.EnvVar, string, error) {
	for _, v := range vars {
		if v.Name == EnvVar && !v.Skipped {
			return vars, "", nil
		}
	}
	path, err := PathFor(service)
	if err != nil {
		return vars, "", err
	}
	vars = append(vars, k8s.EnvVar{Name: EnvVar, Value: path, Source: k8s.SourceSynthetic})
	return vars, path, nil
}
