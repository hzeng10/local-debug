package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"strings"
)

// Helm does not pass hooks through its post-renderer. Keep the upstream archive
// and patch the single 2.31.0 test hook that lacks pull policy and scheduling.
func PatchChart(src, dst string) error {
	f, e := os.Open(src)
	if e != nil {
		return e
	}
	defer f.Close()
	g, e := gzip.NewReader(f)
	if e != nil {
		return e
	}
	defer g.Close()
	r := tar.NewReader(g)
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	w := tar.NewWriter(gz)
	patched := false
	versionOK := false
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return e
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return fmt.Errorf("unsupported Chart archive member")
		}
		b, e := io.ReadAll(r)
		if e != nil {
			return e
		}
		if h.Name == "telepresence-oss/Chart.yaml" {
			var m map[string]any
			docs, e := decodeYAML(b)
			if e != nil || len(docs) != 1 {
				return fmt.Errorf("invalid Chart metadata")
			}
			m = docs[0]
			versionOK = m["version"] == "2.31.0"
		}
		if h.Name == "telepresence-oss/templates/tests/test-connection.yaml" {
			s := string(b)
			needle := "      command: ['wget']"
			if strings.Count(s, needle) != 1 || strings.Count(s, "spec:\n") != 1 {
				return fmt.Errorf("upstream test hook changed; review offline patch")
			}
			s = strings.Replace(s, needle, "      imagePullPolicy: {{ .Values.hooks.busybox.pullPolicy }}\n"+needle, 1)
			s = strings.Replace(s, "spec:\n", `spec:
  {{- with .Values.affinity }}
  affinity: {{ toYaml . | nindent 4 }}
  {{- end }}
  {{- with .Values.tolerations }}
  tolerations: {{ toYaml . | nindent 4 }}
  {{- end }}
`, 1)
			b = []byte(s)
			patched = true
		}
		h.Size = int64(len(b))
		if e = w.WriteHeader(h); e != nil {
			return e
		}
		if _, e = w.Write(b); e != nil {
			return e
		}
	}
	if !patched || !versionOK {
		return fmt.Errorf("offline hook patch only supports official Telepresence 2.31.0")
	}
	if e = w.Close(); e != nil {
		return e
	}
	if e = gz.Close(); e != nil {
		return e
	}
	return os.WriteFile(dst, out.Bytes(), 0600)
}
