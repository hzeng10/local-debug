// Package tp orchestrates the Telepresence CLI. ldbg shells out to the telepresence
// binary rather than reimplementing its daemon protocol: telepresence already solves
// the cluster VPN (connect) and traffic takeover (global/TCP intercept) we need, and
// in Istio ambient ztunnel terminates mTLS at the node so the intercept sees plaintext.
package tp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Client runs a specific telepresence binary.
type Client struct {
	Bin string
}

// New returns a Client. If bin is empty, "telepresence" is resolved from PATH.
func New(bin string) *Client {
	if bin == "" {
		bin = "telepresence"
	}
	return &Client{Bin: bin}
}

// Available reports whether the telepresence binary can be found/executed.
func (c *Client) Available() bool {
	_, err := exec.LookPath(c.Bin)
	return err == nil
}

type runResult struct {
	Stdout string
	Stderr string
	Code   int
}

func (c *Client) run(ctx context.Context, args ...string) (runResult, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	rr := runResult{Stdout: so.String(), Stderr: se.String()}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		rr.Code = ee.ExitCode()
		// Non-zero exit is reported via rr; surface stderr as the error text.
		return rr, fmt.Errorf("telepresence %s: %s", args[0], firstNonEmpty(strings.TrimSpace(se.String()), err.Error()))
	}
	return rr, err
}

// Version returns the telepresence client version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	rr, err := c.run(ctx, "version", "--format", "json")
	if err != nil && rr.Stdout == "" {
		return "", err
	}
	var v struct {
		Client  string `json:"client"`
		Version string `json:"version"`
	}
	if json.Unmarshal([]byte(rr.Stdout), &v) == nil {
		return firstNonEmpty(v.Client, v.Version), nil
	}
	return strings.TrimSpace(rr.Stdout), nil
}

// Status is the distilled connection state ldbg cares about.
type Status struct {
	Connected         bool            `json:"connected"`
	KubernetesContext string          `json:"kubernetesContext,omitempty"`
	Namespace         string          `json:"namespace,omitempty"`
	KubernetesServer  string          `json:"kubernetesServer,omitempty"`
	ManagerInstalled  bool            `json:"managerInstalled"`
	Intercepts        []InterceptInfo `json:"intercepts,omitempty"`
	Err               string          `json:"err,omitempty"`
}

// InterceptInfo is one active intercept.
type InterceptInfo struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

// Status runs `telepresence status --format json` and distills it. A disconnected
// daemon is not an error: it returns Connected=false (with Err populated).
func (c *Client) Status(ctx context.Context) (*Status, error) {
	rr, _ := c.run(ctx, "status", "--format", "json")
	out := strings.TrimSpace(rr.Stdout)
	if out == "" {
		return &Status{}, fmt.Errorf("empty status output (stderr: %s)", strings.TrimSpace(rr.Stderr))
	}

	var top struct {
		Err        string          `json:"err"`
		UserDaemon json.RawMessage `json:"user_daemon"`
	}
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		return &Status{}, fmt.Errorf("parse status json: %w", err)
	}
	st := &Status{}
	if top.Err != "" {
		st.Err = top.Err
		return st, nil // disconnected (no daemon yet)
	}

	var ud struct {
		Running           bool   `json:"running"`
		Status            string `json:"status"`
		KubernetesServer  string `json:"kubernetes_server"`
		KubernetesContext string `json:"kubernetes_context"`
		Namespace         string `json:"namespace"`
		ManagerInstall    *struct {
			Version string `json:"version"`
		} `json:"manager_install,omitempty"`
		TrafficManager *struct {
			Version string `json:"version"`
		} `json:"traffic_manager,omitempty"`
		Intercepts []struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		} `json:"intercepts"`
	}
	if len(top.UserDaemon) > 0 {
		_ = json.Unmarshal(top.UserDaemon, &ud)
	}
	st.Connected = ud.Running && !strings.EqualFold(ud.Status, "Not connected")
	st.KubernetesContext = ud.KubernetesContext
	st.Namespace = ud.Namespace
	st.KubernetesServer = ud.KubernetesServer
	st.ManagerInstalled = ud.ManagerInstall != nil || ud.TrafficManager != nil || st.Connected
	for _, i := range ud.Intercepts {
		st.Intercepts = append(st.Intercepts, InterceptInfo{Name: i.Name, Namespace: i.Namespace})
	}
	return st, nil
}

// ConnectOpts configures `telepresence connect`.
type ConnectOpts struct {
	Namespace        string
	Context          string
	ManagerNamespace string // default "ambassador"
	MappedNamespaces []string
}

// Connect starts the daemons and joins the cluster network. NOTE: starting the root
// daemon requires elevated privileges (sudo on Linux, admin on Windows); in a non-
// interactive context this fails and the returned error explains the one-time elevation.
func (c *Client) Connect(ctx context.Context, o ConnectOpts) error {
	args := []string{"connect"}
	if o.Namespace != "" {
		args = append(args, "--namespace", o.Namespace)
	}
	if o.Context != "" {
		args = append(args, "--context", o.Context)
	}
	if o.ManagerNamespace != "" {
		args = append(args, "--manager-namespace", o.ManagerNamespace)
	}
	if len(o.MappedNamespaces) > 0 {
		args = append(args, "--mapped-namespaces", strings.Join(o.MappedNamespaces, ","))
	}
	_, err := c.run(ctx, args...)
	return err
}

// InterceptOpts configures a global (TCP) intercept = full takeover.
type InterceptOpts struct {
	Name      string // workload/service name
	Namespace string
	Port      string // "<localPort>:<identifier>" (identifier = svc port name/number)
	EnvFile   string // -e: write remote env here
	Mount     string // "true"|"false"|path ; default "false" (we inject env, not mounts)
	Workload  string // -w, if different from Name
	Service   string // --service, to disambiguate a port
}

// Intercept creates a global TCP intercept (no --http-header → full takeover, no
// waypoint and no license required in ambient).
func (c *Client) Intercept(ctx context.Context, o InterceptOpts) error {
	if o.Mount == "" {
		o.Mount = "false"
	}
	args := []string{"intercept", o.Name, "--mechanism", "tcp", "--mount", o.Mount}
	if o.Namespace != "" {
		args = append(args, "--namespace", o.Namespace)
	}
	if o.Port != "" {
		args = append(args, "--port", o.Port)
	}
	if o.EnvFile != "" {
		args = append(args, "--env-file", o.EnvFile)
	}
	if o.Workload != "" {
		args = append(args, "--workload", o.Workload)
	}
	if o.Service != "" {
		args = append(args, "--service", o.Service)
	}
	_, err := c.run(ctx, args...)
	return err
}

// Leave stops a single intercept.
func (c *Client) Leave(ctx context.Context, name string) error {
	_, err := c.run(ctx, "leave", name)
	return err
}

// Uninstall removes the traffic-agent from a workload. NOTE: `telepresence uninstall`
// resolves the workload in the *connected* namespace (it has no --namespace flag), so
// the connection must be scoped to the workload's namespace (ldbg up connects scoped).
// Removing the agent is what lets an ambient workload safely return to the mesh.
func (c *Client) Uninstall(ctx context.Context, workload string) error {
	_, err := c.run(ctx, "uninstall", workload)
	return err
}

// Quit stops the daemons. stopAll passes -s/--stop-daemons to stop both the user and
// root daemons (a plain quit leaves the root daemon running).
func (c *Client) Quit(ctx context.Context, stopAll bool) error {
	args := []string{"quit"}
	if stopAll {
		args = append(args, "--stop-daemons")
	}
	_, err := c.run(ctx, args...)
	return err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
