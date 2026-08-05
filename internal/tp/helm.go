package tp

import (
	"context"
	"fmt"
	"strings"
)

// HelmOpts configures the embedded-chart traffic-manager install. The chart ships
// inside the telepresence client (no internet/helm binary needed). For an air-gapped
// cluster, point Registry/AgentImage at the side-loaded image so the cluster never
// reaches out: the OSS image ghcr.io/telepresenceio/tel2:<ver> serves BOTH the
// traffic-manager and the injected traffic-agent.
type HelmOpts struct {
	ManagerNamespace string   // default "ambassador"
	Registry         string   // registry path holding the tel2 image
	AgentImage       string   // full reference for the injected traffic-agent
	PullPolicy       string   // e.g. "IfNotPresent" for air-gap
	Sets             []string // extra raw --set a=b
}

// HelmInstall runs `telepresence helm install` from the embedded chart. No cluster
// internet access is required when the image is already present (pullPolicy IfNotPresent).
func (c *Client) HelmInstall(ctx context.Context, o HelmOpts) error {
	return c.helm(ctx, "install", o)
}

// HelmUpgrade is HelmInstall's idempotent sibling (install-or-upgrade).
func (c *Client) HelmUpgrade(ctx context.Context, o HelmOpts) error {
	return c.helm(ctx, "upgrade", o)
}

func (c *Client) helm(ctx context.Context, verb string, o HelmOpts) error {
	sets, err := helmSetArgs(o)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, append([]string{"helm", verb}, sets...)...)
	return err
}

// HelmUninstall removes the traffic-manager.
func (c *Client) HelmUninstall(ctx context.Context) error {
	_, err := c.run(ctx, "helm", "uninstall")
	return err
}

// helmSetArgs maps ldbg's options onto the chart's value names.
//
// These names are NOT interchangeable with the older telepresence charts: the
// telepresence-oss chart (2.29.0) has no top-level `images` key at all, and its
// schema sets additionalProperties=false — so an `images.registry` style value
// is rejected outright ("additional properties 'images' not allowed") and
// nothing gets installed. The manager's image comes from `image.*`, and the
// injected agent's from `agent.image.*`, which the manager passes on as the
// AGENT_REGISTRY / AGENT_IMAGE_NAME / AGENT_IMAGE_TAG environment variables.
func helmSetArgs(o HelmOpts) ([]string, error) {
	var args []string
	if o.ManagerNamespace != "" {
		args = append(args, "--namespace", o.ManagerNamespace)
	}
	add := func(kv string) { args = append(args, "--set", kv) }

	// The traffic-manager's own image.
	if o.Registry != "" {
		add("image.registry=" + o.Registry)
	}
	if o.PullPolicy != "" {
		add("image.pullPolicy=" + o.PullPolicy)
	}

	// The injected traffic-agent's image, which the chart takes in three parts.
	if o.AgentImage != "" {
		reg, name, tag, err := splitImageRef(o.AgentImage)
		if err != nil {
			return nil, err
		}
		if reg != "" {
			add("agent.image.registry=" + reg)
		}
		add("agent.image.name=" + name)
		if tag != "" {
			add("agent.image.tag=" + tag)
		}
	} else if o.Registry != "" {
		// No explicit agent image: it lives wherever the manager's image does.
		add("agent.image.registry=" + o.Registry)
	}
	if o.PullPolicy != "" {
		add("agent.image.pullPolicy=" + o.PullPolicy)
	}

	for _, s := range o.Sets {
		if strings.TrimSpace(s) != "" {
			add(s)
		}
	}
	return args, nil
}

// splitImageRef breaks "ghcr.io/telepresenceio/tel2:2.29.0" into the registry
// path, image name and tag the chart wants separately. A registry host may carry
// a port ("harbor.corp:5000/tel/tel2:2.29.0"), so the name/tag colon is only the
// one in the LAST path segment.
func splitImageRef(ref string) (registry, name, tag string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", "", fmt.Errorf("empty image reference")
	}
	last := ref
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		registry, last = ref[:i], ref[i+1:]
	}
	// The chart builds the agent reference as registry/name:tag, so a digest
	// cannot be expressed — say so instead of silently pulling something else.
	if strings.Contains(last, "@") {
		return "", "", "", fmt.Errorf("agent image %q is pinned by digest, which the traffic-manager chart cannot express — use a tagged reference", ref)
	}
	name = last
	if i := strings.LastIndex(last, ":"); i >= 0 {
		name, tag = last[:i], last[i+1:]
	}
	if name == "" {
		return "", "", "", fmt.Errorf("image reference %q has no image name", ref)
	}
	return registry, name, tag, nil
}
