package provision

import (
	"bytes"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"os"
)

type RenderConfig struct {
	Refs        map[string]string `json:"refs"`
	Policy      string            `json:"policy"`
	Nodes       []string          `json:"nodes"`
	Tolerations []map[string]any  `json:"tolerations"`
}

func FilterManifests(b []byte, c RenderConfig) ([]byte, error) {
	docs, e := decodeYAML(b)
	if e != nil {
		return nil, e
	}
	if len(c.Nodes) == 0 || !has([]string{"Never", "IfNotPresent"}, c.Policy) {
		return nil, fmt.Errorf("invalid renderer configuration")
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if _, ok := x["containers"]; ok {
				x["affinity"] = map[string]any{"nodeAffinity": map[string]any{"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{"nodeSelectorTerms": []any{map[string]any{"matchFields": []any{map[string]any{"key": "metadata.name", "operator": "In", "values": c.Nodes}}}}}}}
				if len(c.Tolerations) > 0 {
					x["tolerations"] = c.Tolerations
				}
				for _, k := range []string{"containers", "initContainers", "ephemeralContainers"} {
					for _, v := range arr(x[k]) {
						obj(v)["imagePullPolicy"] = c.Policy
					}
				}
			}
			for _, v := range x {
				visit(v)
			}
		case []any:
			for _, v := range x {
				visit(v)
			}
		}
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	for _, d := range docs {
		visit(d)
		if e = enc.Encode(d); e != nil {
			return nil, e
		}
	}
	if e = enc.Close(); e != nil {
		return nil, e
	}
	if e = auditManifests(out.Bytes(), c.Refs, c.Policy); e != nil {
		return nil, e
	}
	return out.Bytes(), nil
}
func renderCLI(path string, in io.Reader, out, errOut io.Writer) int {
	var c RenderConfig
	if e := readJSON(path, &c); e != nil {
		fmt.Fprintln(errOut, e)
		return 1
	}
	b, e := io.ReadAll(io.LimitReader(in, 32<<20))
	if e == nil {
		b, e = FilterManifests(b, c)
	}
	if e != nil {
		fmt.Fprintln(errOut, e)
		return 1
	}
	if _, e = out.Write(b); e != nil {
		return 1
	}
	return 0
}

var stdin = func() io.Reader { return os.Stdin }
