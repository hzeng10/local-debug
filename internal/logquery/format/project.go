// Local addition (NOT vendored): helpers layered on the vendored output.go so
// that file stays a pristine copy of the upstream for clean re-syncs.
package format

// Projected returns a copy of env with Logs reduced to the requested fields
// (no-op when fields is empty). Used by --json mode, which marshals the
// Envelope inside ldbg's own envelope instead of calling WriteEnvelope.
func Projected(env Envelope, fields []string) Envelope {
	if len(fields) == 0 {
		return env
	}
	logs := make([]map[string]any, len(env.Logs))
	for i, l := range env.Logs {
		logs[i] = project(l, fields)
	}
	env.Logs = logs
	return env
}
