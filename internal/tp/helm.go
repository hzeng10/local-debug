package tp

import (
	"context"
	"strings"
)

// HelmOpts configures the embedded-chart traffic-manager install. The chart ships
// inside the telepresence client (no internet/helm binary needed). For an air-gapped
// cluster, point Registry/AgentImage at the side-loaded image so the cluster never
// reaches out: the OSS image ghcr.io/telepresenceio/tel2:<ver> serves BOTH the
// traffic-manager and the injected traffic-agent.
type HelmOpts struct {
	ManagerNamespace string   // default "ambassador"
	Registry         string   // images.registry (e.g. an internal registry path)
	AgentImage       string   // agent.image.name or images.agentImage override
	PullPolicy       string   // images.pullPolicy, e.g. "IfNotPresent" for air-gap
	Sets             []string // extra raw --set a=b
}

// HelmInstall runs `telepresence helm install` from the embedded chart. No cluster
// internet access is required when the image is already present (pullPolicy IfNotPresent).
func (c *Client) HelmInstall(ctx context.Context, o HelmOpts) error {
	args := []string{"helm", "install"}
	args = append(args, helmSetArgs(o)...)
	_, err := c.run(ctx, args...)
	return err
}

// HelmUpgrade is HelmInstall's idempotent sibling (install-or-upgrade).
func (c *Client) HelmUpgrade(ctx context.Context, o HelmOpts) error {
	args := []string{"helm", "upgrade"}
	args = append(args, helmSetArgs(o)...)
	_, err := c.run(ctx, args...)
	return err
}

// HelmUninstall removes the traffic-manager.
func (c *Client) HelmUninstall(ctx context.Context) error {
	_, err := c.run(ctx, "helm", "uninstall")
	return err
}

func helmSetArgs(o HelmOpts) []string {
	var args []string
	if o.ManagerNamespace != "" {
		args = append(args, "--namespace", o.ManagerNamespace)
	}
	add := func(kv string) { args = append(args, "--set", kv) }
	if o.Registry != "" {
		add("images.registry=" + o.Registry)
	}
	if o.AgentImage != "" {
		add("images.agentImage=" + o.AgentImage)
	}
	if o.PullPolicy != "" {
		add("images.pullPolicy=" + o.PullPolicy)
	}
	for _, s := range o.Sets {
		if strings.TrimSpace(s) != "" {
			add(s)
		}
	}
	return args
}
