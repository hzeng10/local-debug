package provision

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"
)

const usage = `devctl bundle prepare --config bundle.json [--format json]
devctl bundle verify --bundle DIR [--format json]
devctl admin discover|plan|apply|verify|export --config install.json [--format json]
devctl admin remote --operation discover|plan|apply|verify|export --config install.json
Only bundle prepare downloads public artifacts. Admin operations are offline.
`

func Main(args []string, out, errOut io.Writer) int {
	if len(args) < 2 || args[1] == "--help" {
		fmt.Fprint(out, usage)
		return 0
	}
	fs := flag.NewFlagSet("devctl "+args[0]+" "+args[1], flag.ContinueOnError)
	fs.SetOutput(errOut)
	config := fs.String("config", "", "JSON config")
	bundle := fs.String("bundle", "", "offline bundle directory")
	format := fs.String("format", "text", "text or json")
	operation := fs.String("operation", "apply", "remote operation")
	if e := fs.Parse(args[2:]); e != nil {
		return 2
	}
	if fs.NArg() != 0 || (*format != "json" && *format != "text") {
		return 2
	}
	emit := func(v any) {
		enc := json.NewEncoder(out)
		if *format == "text" {
			enc.SetIndent("", "  ")
		}
		_ = enc.Encode(v)
	}
	if args[0] == "admin" && args[1] == "render-filter" {
		return renderCLI(*config, stdin(), out, errOut)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	var result any
	var err error
	r := OSRunner{}
	if args[0] == "bundle" {
		switch args[1] {
		case "prepare":
			result, err = BundlePrepare(ctx, *config, r, errOut)
		case "verify":
			result, err = BundleVerify(*bundle)
		default:
			err = fmt.Errorf("unknown bundle operation")
		}
	} else {
		var c Install
		c, err = loadInstall(*config)
		if err == nil {
			a := Admin{C: c, R: r, Log: errOut}
			switch args[1] {
			case "discover":
				result, err = a.Discover(ctx)
			case "plan":
				result, err = a.Plan(ctx)
			case "apply":
				result, err = a.Apply(ctx)
			case "verify":
				result, err = a.Verify(ctx)
			case "export":
				var fp string
				fp, _, err = a.readCredentials(ctx)
				if err == nil {
					err = a.Export(fp)
				}
				result = map[string]any{"handoff": filepath.Join(c.WorkDir, "handoff")}
			case "remote":
				result, err = Remote(ctx, c, *operation, r, errOut)
			default:
				err = fmt.Errorf("unknown administrator operation")
			}
		}
	}
	if err != nil {
		emit(map[string]any{"ok": false, "error": err.Error()})
		return 1
	}
	emit(result)
	return 0
}
