package cmd

import (
	"context"
	"encoding/json"
	"os"

	"github.com/hzeng10/local-debug/internal/logquery/client"
	"github.com/hzeng10/local-debug/internal/logquery/format"
	"github.com/spf13/cobra"
)

var (
	logsFieldsFlags vlFlags
	logsValuesFlags vlFlags
)

var logsFieldsCmd = &cobra.Command{
	Use:   "fields",
	Short: "List log field names in the store (agent introspection)",
	Long: `fields lists the field names present in logs matching the filters — the
starting point for an agent exploring what can be queried (service, namespace,
pod, container, level, log_type, ...).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFieldIntrospection(cmd, "logs fields", &logsFieldsFlags, func(ctx context.Context, cl *client.Client, q string) (json.RawMessage, error) {
			return cl.FieldNames(ctx, q)
		}, "fields", false)
	},
}

var logsValuesCmd = &cobra.Command{
	Use:   "values <field>",
	Short: "List the values of one log field (agent introspection)",
	Long: `values lists the distinct values of a field in logs matching the filters,
e.g. 'ldbg logs values service' shows every collected service name.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		v := &logsValuesFlags
		return runFieldIntrospection(cmd, "logs values", v, func(ctx context.Context, cl *client.Client, q string) (json.RawMessage, error) {
			return cl.FieldValues(ctx, args[0], q, v.limit)
		}, "values", true)
	},
}

// runFieldIntrospection is the shared body of fields/values: build the filter
// query, resolve the store address, call the endpoint, render raw-or-envelope.
// fields skips the intercept default (the global field universe is the point);
// values keeps it so `ldbg logs values level` scopes to the intercepted service.
func runFieldIntrospection(cmd *cobra.Command, name string, v *vlFlags, call func(context.Context, *client.Client, string) (json.RawMessage, error), key string, withDefaults bool) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	st := tpStatusQuick(ctx)
	f := v.filter()
	if withDefaults {
		applyLogDefaults(&f, st, flagNamespace)
	} else if flagNamespace != "" {
		f.Namespace = flagNamespace
	}

	q, rng, err := f.Build()
	if err != nil {
		return out.Failf(name, "check --since/--from/--to format", err)
	}

	addr, cleanup, _, err := resolveVLogs(ctx, v, st)
	if err != nil {
		return out.Failf(name, vlogsHint, err)
	}
	defer cleanup()

	qctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	raw, err := call(qctx, client.New(addr, v.timeout), q)
	if err != nil {
		return out.Failf(name, vlogsHint, err)
	}

	if flagJSON {
		out.Result(name, "", map[string]any{"query": q, "range": rng, key: json.RawMessage(raw)})
		return nil
	}
	return format.WriteRaw(os.Stdout, raw)
}

func init() {
	logsFieldsFlags.register(logsFieldsCmd, true, false)
	logsValuesFlags.register(logsValuesCmd, true, true)
	logsCmd.AddCommand(logsFieldsCmd, logsValuesCmd)
}
