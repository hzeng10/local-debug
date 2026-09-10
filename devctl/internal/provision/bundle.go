package provision

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func fetch(ctx context.Context, u string, w io.Writer, log io.Writer) error {
	parsed, e := url.Parse(u)
	if e != nil || parsed.Scheme != "https" || parsed.User != nil {
		return fmt.Errorf("download requires HTTPS without inline credentials")
	}
	req, e := http.NewRequestWithContext(ctx, "GET", u, nil)
	if e != nil {
		return e
	}
	req.Header.Set("User-Agent", "devctl-offline/0.2")
	// Clone so diagnostics do not mutate the shared transport. The standard
	// selector honors HTTP(S)_PROXY, lowercase equivalents and NO_PROXY for
	// every request, including a release asset's redirected CDN hostname.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(r *http.Request) (*url.URL, error) {
		proxy, err := http.ProxyFromEnvironment(r)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy environment; check HTTP_PROXY/HTTPS_PROXY")
		}
		route := "direct (no matching proxy, NO_PROXY bypass, or loopback)"
		if proxy != nil {
			// Neither proxy userinfo nor signed download URLs belong in logs.
			route = "proxy " + proxy.Scheme + "://" + proxy.Host
		}
		fmt.Fprintf(log, "download %s via %s\n", r.URL.Host, route)
		return proxy, nil
	}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: 10 * time.Minute, CheckRedirect: func(r *http.Request, v []*http.Request) error {
		if len(v) > 8 || r.URL.Scheme != "https" || r.URL.User != nil {
			return fmt.Errorf("unsafe download redirect")
		}
		return nil
	}}
	resp, e := client.Do(req)
	if e != nil {
		// url.Error may contain signed redirect URLs; keep the underlying cause.
		var ue *url.Error
		if errors.As(e, &ue) {
			e = ue.Err
		}
		return fmt.Errorf("download %s failed: %v; check the route above, HTTP(S)_PROXY / NO_PROXY, proxy port/protocol and trusted CA certificates (HTTPS_PROXY commonly uses http://)", parsed.Host, e)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: HTTP %d", parsed.Host, resp.StatusCode)
	}
	_, e = io.Copy(w, resp.Body)
	return e
}
func fetchBytes(ctx context.Context, u string, log io.Writer) ([]byte, error) {
	var b bytes.Buffer
	e := fetch(ctx, u, &b, log)
	return b.Bytes(), e
}
func resolveArtifact(ctx context.Context, a Artifact, log io.Writer) (Artifact, error) {
	if a.HelmIndexURL != "" {
		b, e := fetchBytes(ctx, a.HelmIndexURL, log)
		if e != nil {
			return a, e
		}
		var idx struct {
			Entries map[string][]struct {
				Version string   `yaml:"version"`
				Digest  string   `yaml:"digest"`
				URLs    []string `yaml:"urls"`
			} `yaml:"entries"`
		}
		if e = yaml.Unmarshal(b, &idx); e != nil {
			return a, e
		}
		found := false
		for _, v := range idx.Entries[a.ChartName] {
			if v.Version == a.ChartVersion && len(v.URLs) > 0 {
				base, _ := url.Parse(a.HelmIndexURL)
				u, e := base.Parse(v.URLs[0])
				if e != nil {
					return a, e
				}
				a.URL = u.String()
				a.SHA256 = v.Digest
				found = true
				break
			}
		}
		if !found {
			return a, fmt.Errorf("chart %s %s missing from index", a.ChartName, a.ChartVersion)
		}
	}
	if a.SHA256 == "" && a.GitHubAssetAPI != "" {
		b, e := fetchBytes(ctx, a.GitHubAssetAPI, log)
		if e != nil {
			return a, e
		}
		var release struct {
			Assets []struct {
				Name   string `json:"name"`
				Digest string `json:"digest"`
			}
		}
		if e = json.Unmarshal(b, &release); e != nil {
			return a, e
		}
		u, _ := url.Parse(a.URL)
		for _, x := range release.Assets {
			if x.Name == filepath.Base(u.Path) {
				a.SHA256 = strings.TrimPrefix(x.Digest, "sha256:")
			}
		}
	}
	if a.SHA256 == "" && a.ChecksumURL != "" {
		b, e := fetchBytes(ctx, a.ChecksumURL, log)
		if e != nil {
			return a, e
		}
		u, _ := url.Parse(a.URL)
		want := filepath.Base(u.Path)
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 1 && shaRE.MatchString(f[0]) {
				a.SHA256 = f[0]
			}
			if len(f) >= 2 && strings.TrimPrefix(f[len(f)-1], "*") == want && shaRE.MatchString(f[0]) {
				a.SHA256 = f[0]
			}
		}
	}
	if !shaRE.MatchString(a.SHA256) {
		return a, fmt.Errorf("artifact %s needs an upstream SHA256 (explicit sha256 or checksum source)", a.ID)
	}
	return a, nil
}
func extractArtifact(src, dest string, a Artifact) error {
	if e := os.MkdirAll(filepath.Dir(dest), 0700); e != nil {
		return e
	}
	f, e := os.Open(src)
	if e != nil {
		return e
	}
	defer f.Close()
	write := func(r io.Reader) error {
		o, e := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
		if e != nil {
			return e
		}
		_, e = io.Copy(o, r)
		ce := o.Close()
		if e != nil {
			return e
		}
		return ce
	}
	switch a.Format {
	case "", "file":
		return write(f)
	case "gz":
		g, e := gzip.NewReader(f)
		if e != nil {
			return e
		}
		defer g.Close()
		return write(g)
	case "tar.gz":
		g, e := gzip.NewReader(f)
		if e != nil {
			return e
		}
		defer g.Close()
		t := tar.NewReader(g)
		for {
			h, e := t.Next()
			if e == io.EOF {
				break
			}
			if e != nil {
				return e
			}
			if h.Name == a.Member {
				if h.Typeflag != tar.TypeReg {
					return fmt.Errorf("artifact member must be regular file")
				}
				return write(t)
			}
		}
	case "zip":
		z, e := zip.OpenReader(src)
		if e != nil {
			return e
		}
		defer z.Close()
		for _, x := range z.File {
			if x.Name == a.Member {
				if !x.Mode().IsRegular() {
					return fmt.Errorf("artifact member must be regular file")
				}
				r, e := x.Open()
				if e != nil {
					return e
				}
				defer r.Close()
				return write(r)
			}
		}
	default:
		return fmt.Errorf("unsupported artifact format %q", a.Format)
	}
	return fmt.Errorf("member %q absent from artifact %s", a.Member, a.ID)
}
func BundlePrepare(ctx context.Context, path string, r Runner, log io.Writer) (Lock, error) {
	var c BundleConfig
	var lock Lock
	if e := readJSON(path, &c); e != nil {
		return lock, e
	}
	if c.Version != 1 || len(c.Artifacts) == 0 || len(c.Images) == 0 || c.Output == "" || c.Cache == "" || c.Distribution == "" {
		return lock, fmt.Errorf("incomplete bundle config")
	}
	base := filepath.Dir(path)
	c.Output = resolve(base, c.Output)
	c.Cache = resolve(base, c.Cache)
	c.Distribution = resolve(base, c.Distribution)
	if entries, e := os.ReadDir(c.Output); e == nil && len(entries) > 0 {
		return lock, fmt.Errorf("output already contains a bundle; use a new output directory")
	}
	if e := secureDir(c.Output); e != nil {
		return lock, e
	}
	if e := secureDir(c.Cache); e != nil {
		return lock, e
	}
	lock.Version = 1
	seen := map[string]bool{}
	add := func(p, source, upstream string) error {
		if seen[p] {
			return fmt.Errorf("duplicate bundle path %s", p)
		}
		seen[p] = true
		full, e := relative(c.Output, p)
		if e != nil {
			return e
		}
		s, e := fileSHA(full)
		if e != nil {
			return e
		}
		lock.Files = append(lock.Files, LockedFile{p, s, source, upstream})
		return nil
	}
	for _, a := range c.Artifacts {
		if a.HelmOCI != "" {
			crane := tool(c.Output, "crane")
			b, e := r.Run(ctx, []string{crane, "digest", a.HelmOCI}, nil, nil)
			if e != nil {
				return lock, e
			}
			manifestDigest := strings.TrimSpace(string(b))
			if !validDigest(manifestDigest) {
				return lock, fmt.Errorf("invalid chart manifest digest")
			}
			ref := imageRepository(a.HelmOCI) + "@" + manifestDigest
			b, e = r.Run(ctx, []string{crane, "manifest", ref}, nil, nil)
			if e != nil {
				return lock, e
			}
			var m struct {
				Layers []struct {
					MediaType string `json:"mediaType"`
					Digest    string `json:"digest"`
				} `json:"layers"`
			}
			if e = json.Unmarshal(b, &m); e != nil {
				return lock, e
			}
			digest := ""
			for _, l := range m.Layers {
				if l.MediaType == "application/vnd.cncf.helm.chart.content.v1.tar+gzip" {
					digest = l.Digest
				}
			}
			if !validDigest(digest) {
				return lock, fmt.Errorf("Helm chart content layer missing")
			}
			b, e = r.Run(ctx, []string{crane, "blob", imageRepository(a.HelmOCI) + "@" + digest}, nil, nil)
			if e != nil {
				return lock, e
			}
			sum := sha256.Sum256(b)
			a.SHA256 = strings.TrimPrefix(digest, "sha256:")
			if hex.EncodeToString(sum[:]) != a.SHA256 {
				return lock, fmt.Errorf("chart layer checksum mismatch")
			}
			a.File = filepath.Join(c.Cache, a.SHA256)
			a.URL = "oci://" + ref
			if e = os.WriteFile(a.File, b, 0600); e != nil {
				return lock, e
			}
		}
		fmt.Fprintf(log, "prepare artifact %s\n", a.ID)
		target, e := relative(c.Output, a.Target)
		if e != nil {
			return lock, e
		}
		if a.File != "" {
			a.File = resolve(base, a.File)
			if !shaRE.MatchString(a.SHA256) {
				return lock, fmt.Errorf("local artifact %s requires sha256", a.ID)
			}
		} else {
			a, e = resolveArtifact(ctx, a, log)
			if e != nil {
				return lock, e
			}
			a.File = filepath.Join(c.Cache, a.SHA256)
			sum, _ := fileSHA(a.File)
			if sum != a.SHA256 {
				temp := a.File + ".part"
				f, e := os.Create(temp)
				if e != nil {
					return lock, e
				}
				e = fetch(ctx, a.URL, f, log)
				ce := f.Close()
				if e == nil {
					e = ce
				}
				if e != nil {
					os.Remove(temp)
					return lock, e
				}
				sum, e = fileSHA(temp)
				if e != nil || sum != a.SHA256 {
					os.Remove(temp)
					return lock, fmt.Errorf("upstream checksum mismatch for %s", a.ID)
				}
				if e = os.Rename(temp, a.File); e != nil {
					return lock, e
				}
			}
		}
		sum, e := fileSHA(a.File)
		if e != nil || sum != a.SHA256 {
			return lock, fmt.Errorf("checksum mismatch: %s", a.ID)
		}
		if e = extractArtifact(a.File, target, a); e != nil {
			return lock, e
		}
		if e = add(a.Target, a.URL, a.SHA256); e != nil {
			return lock, e
		}
	}
	upstream := filepath.Join(c.Output, "charts", "telepresence-upstream.tgz")
	if e := PatchChart(upstream, filepath.Join(c.Output, "charts", "telepresence.tgz")); e != nil {
		return lock, e
	}
	if e := add("charts/telepresence.tgz", "derived:telepresence-2.31.0-offline-hook-v1", ""); e != nil {
		return lock, e
	}
	// Only distributable files are copied. Personal profiles, logs and credentials are excluded.
	for _, folder := range []string{"deploy", "docs", "examples", "scripts"} {
		root := filepath.Join(c.Distribution, folder)
		e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("distribution contains non-regular file")
			}
			if strings.Contains(d.Name(), ".local.") || strings.HasSuffix(d.Name(), ".log") {
				return nil
			}
			rel, _ := filepath.Rel(c.Distribution, p)
			rel = filepath.ToSlash(rel)
			dst, e := relative(c.Output, rel)
			if e != nil {
				return e
			}
			if e = extractArtifact(p, dst, Artifact{}); e != nil {
				return e
			}
			return add(rel, "distribution", "")
		})
		if e != nil {
			return lock, e
		}
	}
	for _, f := range []struct{ src, dst string }{{"README.md", "README.md"}, {"dist/devctl-linux-amd64", "bin/linux-amd64/devctl"}, {"dist/devctl-linux-arm64", "bin/linux-arm64/devctl"}, {"dist/devctl-windows-amd64.exe", "bin/windows-amd64/devctl.exe"}, {"dist/devctl-windows-arm64.exe", "bin/windows-arm64/devctl.exe"}} {
		src := filepath.Join(c.Distribution, f.src)
		if _, e := os.Stat(src); e != nil {
			if strings.Contains(f.src, "arm64") {
				continue
			}
			return lock, e
		}
		dst, _ := relative(c.Output, f.dst)
		if e := extractArtifact(src, dst, Artifact{}); e != nil {
			return lock, e
		}
		if e := add(f.dst, "distribution", ""); e != nil {
			return lock, e
		}
	}
	crane := tool(c.Output, "crane")
	for _, im := range c.Images {
		if !nameRE.MatchString(im.Name) || !has([]string{"linux/amd64", "linux/arm64"}, im.Platform) {
			return lock, fmt.Errorf("invalid image or platform")
		}
		dst, e := relative(c.Output, im.File)
		if e != nil {
			return lock, e
		}
		if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
			return lock, e
		}
		fmt.Fprintf(log, "prepare image %s %s\n", im.Name, im.Platform)
		b, e := r.Run(ctx, []string{crane, "digest", "--platform", im.Platform, im.Source}, nil, nil)
		if e != nil {
			return lock, e
		}
		im.SourceDigest = strings.TrimSpace(string(b))
		if !validDigest(im.SourceDigest) {
			return lock, fmt.Errorf("invalid source image digest")
		}
		src := imageRepository(im.Source) + "@" + im.SourceDigest
		if _, e = r.Run(ctx, []string{crane, "pull", "--platform", im.Platform, src, dst}, nil, nil); e != nil {
			return lock, e
		}
		im.Ref = "devctl.local/" + im.Name + ":" + strings.TrimPrefix(im.SourceDigest, "sha256:")[:16] + "-" + strings.TrimPrefix(im.Platform, "linux/")
		im.ConfigDigest, e = retagArchive(dst, im.Ref)
		if e != nil {
			return lock, e
		}
		b, e = r.Run(ctx, []string{crane, "digest", "--tarball", dst}, nil, nil)
		if e != nil {
			return lock, e
		}
		im.Digest = strings.TrimSpace(string(b))
		if !validDigest(im.Digest) {
			return lock, fmt.Errorf("invalid archive digest")
		}
		lock.Images = append(lock.Images, im)
		if e = add(im.File, src, ""); e != nil {
			return lock, e
		}
	}
	sort.Slice(lock.Files, func(i, j int) bool { return lock.Files[i].Path < lock.Files[j].Path })
	if e := writeJSON(filepath.Join(c.Output, "bundle.lock.json"), lock); e != nil {
		return lock, e
	}
	var sums strings.Builder
	for _, f := range lock.Files {
		fmt.Fprintf(&sums, "%s  %s\n", f.SHA256, f.Path)
	}
	h, _ := fileSHA(filepath.Join(c.Output, "bundle.lock.json"))
	fmt.Fprintf(&sums, "%s  bundle.lock.json\n", h)
	if e := os.WriteFile(filepath.Join(c.Output, "SHA256SUMS"), []byte(sums.String()), 0600); e != nil {
		return lock, e
	}
	return BundleVerify(c.Output)
}
func tool(root, name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "bin", runtime.GOOS+"-"+runtime.GOARCH, name)
}
func validDigest(s string) bool {
	return strings.HasPrefix(s, "sha256:") && shaRE.MatchString(strings.TrimPrefix(s, "sha256:"))
}
func imageRepository(s string) string {
	s = strings.Split(s, "@")[0]
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		s = s[:i]
	}
	return s
}
func BundleVerify(root string) (Lock, error) {
	var l Lock
	if e := readJSON(filepath.Join(root, "bundle.lock.json"), &l); e != nil {
		return l, e
	}
	if l.Version != 1 || len(l.Files) == 0 {
		return l, fmt.Errorf("invalid bundle lock")
	}
	seen := map[string]bool{}
	for _, f := range l.Files {
		p, e := relative(root, f.Path)
		if e != nil {
			return l, e
		}
		if seen[f.Path] || !shaRE.MatchString(f.SHA256) {
			return l, fmt.Errorf("invalid/duplicate locked path")
		}
		seen[f.Path] = true
		// Reject symlinks in every path component, including directories.
		current := root
		for _, part := range strings.Split(f.Path, "/") {
			current = filepath.Join(current, part)
			st, e := os.Lstat(current)
			if e != nil {
				return l, e
			}
			if st.Mode()&os.ModeSymlink != 0 {
				return l, fmt.Errorf("bundle symlink rejected")
			}
		}
		sum, e := fileSHA(p)
		if e != nil || sum != f.SHA256 {
			return l, fmt.Errorf("bundle checksum mismatch: %s", f.Path)
		}
	}
	if e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if rel != "bundle.lock.json" && rel != "SHA256SUMS" && !seen[rel] {
			return fmt.Errorf("unlisted bundle file: %s", rel)
		}
		return nil
	}); e != nil {
		return l, e
	}
	keys := map[string]bool{}
	for _, im := range l.Images {
		key := im.Name + "/" + im.Platform
		if keys[key] || !seen[im.File] || !validDigest(im.Digest) || !validDigest(im.ConfigDigest) || !strings.HasPrefix(im.Ref, "devctl.local/") {
			return l, fmt.Errorf("invalid image lock")
		}
		keys[key] = true
	}
	return l, nil
}

// Rewrite only docker-archive RepoTags. Layers and image config remain unchanged.
func retagArchive(path, ref string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	tmp := path + ".retag"
	o, e := os.Create(tmp)
	if e != nil {
		return "", e
	}
	defer os.Remove(tmp)
	w := tar.NewWriter(o)
	r := tar.NewReader(f)
	config := map[string]string{}
	configPath := ""
	for {
		h, e := r.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			o.Close()
			return "", e
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			o.Close()
			return "", fmt.Errorf("non-regular image archive member")
		}
		if h.Name == "manifest.json" {
			b, e := io.ReadAll(io.LimitReader(r, 4<<20))
			if e != nil {
				o.Close()
				return "", e
			}
			var m []struct {
				Config   string
				RepoTags []string
				Layers   []string
			}
			if e = json.Unmarshal(b, &m); e != nil || len(m) != 1 {
				o.Close()
				return "", fmt.Errorf("expected single image archive")
			}
			m[0].RepoTags = []string{ref}
			configPath = m[0].Config
			b, _ = json.Marshal(m)
			h.Size = int64(len(b))
			if e = w.WriteHeader(h); e == nil {
				_, e = w.Write(b)
			}
			if e != nil {
				o.Close()
				return "", e
			}
			continue
		}
		if e = w.WriteHeader(h); e != nil {
			o.Close()
			return "", e
		}
		if strings.HasSuffix(h.Name, ".json") && h.Size < 16<<20 {
			b, e := io.ReadAll(r)
			if e != nil {
				o.Close()
				return "", e
			}
			sum := sha256.Sum256(b)
			config[h.Name] = "sha256:" + hex.EncodeToString(sum[:])
			if _, e = w.Write(b); e != nil {
				o.Close()
				return "", e
			}
		} else {
			if _, e = io.Copy(w, r); e != nil {
				o.Close()
				return "", e
			}
		}
	}
	if e = w.Close(); e != nil {
		o.Close()
		return "", e
	}
	if e = o.Close(); e != nil {
		return "", e
	}
	f.Close()
	if config[configPath] == "" {
		return "", fmt.Errorf("missing image config")
	}
	if e = os.Rename(tmp, path); e != nil {
		return "", e
	}
	return config[configPath], nil
}
