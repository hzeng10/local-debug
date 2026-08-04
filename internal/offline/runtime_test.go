package offline

import (
	"strings"
	"testing"
)

func TestRuntimeOf(t *testing.T) {
	cases := map[string]Runtime{
		"containerd": RuntimeContainerd,
		"docker":     RuntimeDocker,
		"cri-o":      RuntimeCRIO,
		"crio":       RuntimeCRIO,
		"CONTAINERD": RuntimeContainerd,
		" docker ":   RuntimeDocker,
		"":           RuntimeUnknown,
		"podman":     RuntimeUnknown,
	}
	for in, want := range cases {
		if got := RuntimeOf(in); got != want {
			t.Errorf("RuntimeOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestImportCmd(t *testing.T) {
	// containerd must import into the k8s.io namespace, or the load succeeds and
	// the kubelet still cannot see the image — the classic silent failure.
	got, err := ImportCmd(RuntimeContainerd, "/tmp/tel2.tar", true, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-n k8s.io", "images import", "sudo ctr", "k3s ctr", "nerdctl"} {
		if !strings.Contains(got, want) {
			t.Errorf("containerd import %q must contain %q", got, want)
		}
	}

	got, _ = ImportCmd(RuntimeDocker, "/tmp/tel2.tar", true, "")
	if got != `sudo docker load -i "/tmp/tel2.tar"` {
		t.Errorf("docker import = %q", got)
	}
	got, _ = ImportCmd(RuntimeDocker, "/tmp/tel2.tar", false, "")
	if strings.Contains(got, "sudo") {
		t.Errorf("--sudo=false must not add sudo: %q", got)
	}
	got, _ = ImportCmd(RuntimeCRIO, "/tmp/tel2.tar", true, "")
	if !strings.Contains(got, "podman load") {
		t.Errorf("cri-o import = %q", got)
	}

	// An unknown runtime must refuse rather than run a guess on a cluster node…
	if _, err := ImportCmd(RuntimeUnknown, "/tmp/tel2.tar", true, ""); err == nil {
		t.Error("unknown runtime must not produce a command")
	}
	// …unless the user supplied the command themselves.
	got, err = ImportCmd(RuntimeUnknown, "/tmp/tel2.tar", true, "my-loader import %s --now")
	if err != nil {
		t.Fatal(err)
	}
	if got != "sudo my-loader import /tmp/tel2.tar --now" {
		t.Errorf("override with placeholder = %q", got)
	}
	// An override without a placeholder gets the path appended.
	got, _ = ImportCmd(RuntimeUnknown, "/tmp/tel2.tar", false, "my-loader load")
	if got != "my-loader load /tmp/tel2.tar" {
		t.Errorf("override without placeholder = %q", got)
	}
}

func TestVerifyCmd(t *testing.T) {
	got, err := VerifyCmd(RuntimeContainerd, "ghcr.io/telepresenceio/tel2:2.29.0", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"-n k8s.io", "images ls -q", "grep -qF", "tel2:2.29.0"} {
		if !strings.Contains(got, want) {
			t.Errorf("containerd verify %q must contain %q", got, want)
		}
	}
	if got, _ := VerifyCmd(RuntimeDocker, "img:1", false); !strings.Contains(got, "docker image inspect") {
		t.Errorf("docker verify = %q", got)
	}
	if got, _ := VerifyCmd(RuntimeCRIO, "img:1", false); !strings.Contains(got, "crictl images") {
		t.Errorf("cri-o verify = %q", got)
	}
	if _, err := VerifyCmd(RuntimeUnknown, "img:1", false); err == nil {
		t.Error("unknown runtime must not produce a verify command")
	}
}

func TestSSHTargetAndRemotePath(t *testing.T) {
	if got := sshTarget("10.0.0.1", "root"); got != "root@10.0.0.1" {
		t.Errorf("sshTarget = %q", got)
	}
	// An address that already names a user wins over the default.
	if got := sshTarget("admin@10.0.0.1", "root"); got != "admin@10.0.0.1" {
		t.Errorf("sshTarget = %q", got)
	}
	if got := sshTarget("10.0.0.1", ""); got != "10.0.0.1" {
		t.Errorf("sshTarget = %q", got)
	}
	if got := remotePath("", "/home/me/tel2-bundle.tar"); got != "/tmp/tel2-bundle.tar" {
		t.Errorf("remotePath = %q", got)
	}
	if got := remotePath("/var/tmp/", "dist/tel2-bundle-arm64.tar"); got != "/var/tmp/tel2-bundle-arm64.tar" {
		t.Errorf("remotePath = %q", got)
	}
}

func TestPushDestination(t *testing.T) {
	img := ImageFor("2.29.0")
	if got := PushDestination("harbor.corp/tel", img); got != "harbor.corp/tel/tel2:2.29.0" {
		t.Errorf("PushDestination = %q", got)
	}
	if got := PushDestination("harbor.corp/tel/", img); got != "harbor.corp/tel/tel2:2.29.0" {
		t.Errorf("trailing slash not handled: %q", got)
	}
}
