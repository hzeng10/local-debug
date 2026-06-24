// Package offline implements the air-gapped install path: the traffic-manager and
// injected traffic-agent both run ghcr.io/telepresenceio/tel2:<ver>, a single image.
// On an internet-connected machine `ldbg bundle` pulls + docker-saves it to a tarball;
// inside the air-gapped environment `ldbg cluster install` imports it (internal
// registry or minikube/kind/k3d/ctr) and installs the traffic-manager from the
// telepresence client's embedded Helm chart with pullPolicy=IfNotPresent.
package offline

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ImageFor returns the OSS traffic-manager/agent image for a Telepresence version.
func ImageFor(version string) string {
	v := strings.TrimPrefix(version, "v")
	return "ghcr.io/telepresenceio/tel2:" + v
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		return so.String(), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "),
			firstNonEmpty(strings.TrimSpace(se.String()), err.Error()))
	}
	return so.String(), nil
}

// DockerPull fetches the image on an internet-connected machine.
func DockerPull(ctx context.Context, image string) error {
	_, err := run(ctx, "docker", "pull", image)
	return err
}

// DockerSave writes the image to a transfer tarball.
func DockerSave(ctx context.Context, image, outPath string) error {
	_, err := run(ctx, "docker", "save", image, "-o", outPath)
	return err
}

// Importer describes how to load the bundled image into the air-gapped cluster.
type Importer string

const (
	ImportMinikube Importer = "minikube"
	ImportKind     Importer = "kind"
	ImportK3d      Importer = "k3d"
	ImportCtr      Importer = "ctr"      // containerd on each node (manual scp first)
	ImportRegistry Importer = "registry" // push to an internal registry
)

// ImportBundle loads the tarball/image into the cluster per the chosen method.
// For registry, image is re-tagged under registryPath and pushed; the caller then
// installs with images.registry=registryPath.
func ImportBundle(ctx context.Context, method Importer, tarPath, image, registryPath string) (string, error) {
	switch method {
	case ImportMinikube:
		return run(ctx, "minikube", "image", "load", tarPath)
	case ImportKind:
		return run(ctx, "kind", "load", "image-archive", tarPath)
	case ImportK3d:
		// k3d import works on an image tar.
		return run(ctx, "k3d", "image", "import", tarPath)
	case ImportRegistry:
		if registryPath == "" {
			return "", fmt.Errorf("registry import needs --registry <host/path>")
		}
		dst := registryPath + "/" + lastPathSegment(image)
		if _, err := run(ctx, "docker", "load", "-i", tarPath); err != nil {
			return "", err
		}
		if _, err := run(ctx, "docker", "tag", image, dst); err != nil {
			return "", err
		}
		return run(ctx, "docker", "push", dst)
	case ImportCtr:
		return "", fmt.Errorf("ctr import is per-node: scp %s to each node and run 'ctr -n k8s.io images import %s'", tarPath, tarPath)
	default:
		return "", fmt.Errorf("unknown import method %q", method)
	}
}

func lastPathSegment(image string) string {
	if i := strings.LastIndex(image, "/"); i >= 0 {
		return image[i+1:]
	}
	return image
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
