package provision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func packTree(root, dest string) error {
	f, e := os.Create(dest)
	if e != nil {
		return e
	}
	g := gzip.NewWriter(f)
	t := tar.NewWriter(g)
	e = filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("non-regular bundle member")
		}
		rel, _ := filepath.Rel(root, p)
		s, e := d.Info()
		if e != nil {
			return e
		}
		h := &tar.Header{Name: filepath.ToSlash(rel), Size: s.Size(), Mode: 0700}
		if e = t.WriteHeader(h); e != nil {
			return e
		}
		in, e := os.Open(p)
		if e != nil {
			return e
		}
		_, e = io.Copy(t, in)
		in.Close()
		return e
	})
	te := t.Close()
	ge := g.Close()
	fe := f.Close()
	for _, x := range []error{e, te, ge, fe} {
		if x != nil {
			return x
		}
	}
	return nil
}
func unpackReport(b []byte, root string) error {
	if e := secureDir(root); e != nil {
		return e
	}
	g, e := gzip.NewReader(bytes.NewReader(b))
	if e != nil {
		return e
	}
	defer g.Close()
	t := tar.NewReader(g)
	for {
		h, e := t.Next()
		if e == io.EOF {
			return nil
		}
		if e != nil {
			return e
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			return fmt.Errorf("report symlink rejected")
		}
		p, e := relative(root, name)
		if e != nil {
			return e
		}
		if h.Size > 32<<20 {
			return fmt.Errorf("report member too large")
		}
		if e = secureDir(filepath.Dir(p)); e != nil {
			return e
		}
		f, e := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if e != nil {
			return e
		}
		_, e = io.Copy(f, t)
		f.Close()
		if e != nil {
			return e
		}
	}
}
func Remote(ctx context.Context, c Install, operation string, r Runner, log io.Writer) (any, error) {
	if !has([]string{"discover", "plan", "apply", "verify", "export"}, operation) {
		return nil, fmt.Errorf("invalid remote operation")
	}
	if e := c.SSH.validate(); e != nil {
		return nil, e
	}
	if !regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`).MatchString(c.RemoteDir) || strings.Contains(c.RemoteDir, "..") {
		return nil, fmt.Errorf("remoteDir must be a simple absolute path")
	}
	if _, e := BundleVerify(c.Bundle); e != nil {
		return nil, e
	}
	if e := secureDir(c.WorkDir); e != nil {
		return nil, e
	}
	plain := c.SSH
	plain.Sudo = false
	// The SSH user owns the upload directory; only administrator operations use sudo.
	if _, e := runSSH(ctx, r, plain, []string{"mkdir", "-p", "-m", "700", c.RemoteDir}, nil); e != nil {
		return nil, e
	}
	archive := filepath.Join(c.WorkDir, "upload.tar.gz")
	if e := packTree(c.Bundle, archive); e != nil {
		return nil, e
	}
	defer os.Remove(archive)
	fmt.Fprintln(log, "upload verified offline bundle")
	if e := copySSH(ctx, r, plain, archive, c.RemoteDir+"/bundle.tar.gz"); e != nil {
		return nil, e
	}
	if _, e := runSSH(ctx, r, plain, []string{"mkdir", "-p", "-m", "700", c.RemoteDir + "/bundle"}, nil); e != nil {
		return nil, e
	}
	if _, e := runSSH(ctx, r, plain, []string{"tar", "-xzf", c.RemoteDir + "/bundle.tar.gz", "-C", c.RemoteDir + "/bundle", "--no-same-owner"}, nil); e != nil {
		return nil, e
	}
	remoteC := c
	remoteC.Bundle = c.RemoteDir + "/bundle"
	remoteC.WorkDir = c.RemoteDir + "/state"
	if c.Kubectl == tool(c.Bundle, "kubectl") {
		remoteC.Kubectl = ""
	}
	if c.DeveloperProfile != "" {
		if e := copySSH(ctx, r, plain, c.DeveloperProfile, c.RemoteDir+"/profile.json"); e != nil {
			return nil, e
		}
		remoteC.DeveloperProfile = c.RemoteDir + "/profile.json"
	}
	if c.Registry.ConfigFile != "" {
		if e := copySSH(ctx, r, plain, c.Registry.ConfigFile, c.RemoteDir+"/registry.json"); e != nil {
			return nil, e
		}
		remoteC.Registry.ConfigFile = c.RemoteDir + "/registry.json"
		defer runSSH(context.Background(), r, plain, []string{"rm", "-f", c.RemoteDir + "/registry.json"}, nil)
	}
	localConfig := filepath.Join(c.WorkDir, "remote-install.json")
	if e := writeJSON(localConfig, remoteC); e != nil {
		return nil, e
	}
	if e := copySSH(ctx, r, plain, localConfig, c.RemoteDir+"/install.json"); e != nil {
		return nil, e
	}
	b, e := runSSH(ctx, r, plain, []string{"uname", "-m"}, nil)
	if e != nil {
		return nil, e
	}
	arch := ""
	switch strings.TrimSpace(string(b)) {
	case "x86_64":
		arch = "amd64"
	case "aarch64", "arm64":
		arch = "arm64"
	default:
		return nil, fmt.Errorf("unsupported remote architecture")
	}
	bin := remoteC.Bundle + "/bin/linux-" + arch + "/devctl"
	b, e = runSSH(ctx, r, c.SSH, []string{bin, "admin", operation, "--config", c.RemoteDir + "/install.json", "--format", "json"}, nil)
	if len(b) > 0 {
		_ = os.WriteFile(filepath.Join(c.WorkDir, operation+".json"), b, 0600)
	}
	var report any
	if json.Unmarshal(b, &report) != nil {
		if e != nil {
			return nil, e
		}
		return nil, fmt.Errorf("remote command returned invalid JSON")
	}
	if e != nil {
		return report, fmt.Errorf("remote %s failed; see local %s.json and remote %s/failure.json", operation, operation, remoteC.WorkDir)
	}
	if operation == "apply" || operation == "export" {
		handoff, e := runSSH(ctx, r, c.SSH, []string{"tar", "-czf", "-", "-C", remoteC.WorkDir, "handoff"}, nil)
		if e != nil {
			return nil, e
		}
		if e = unpackReport(handoff, c.WorkDir); e != nil {
			return nil, e
		}
	}
	return report, nil
}
