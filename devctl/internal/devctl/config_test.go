package devctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeReader map[string]string

func (f fakeReader) Get(_ context.Context, ns, kind, name string, out any) error {
	b, ok := f[ns+"/"+kind+"/"+name]
	if !ok {
		return fmt.Errorf("forbidden or missing")
	}
	return json.Unmarshal([]byte(b), out)
}
func configFixture() (Profile, fakeReader) {
	p := Profile{Version: 1, Name: "order", Cluster: Cluster{Namespace: "test"}, Source: Source{Deployment: "order", Container: "app", EnvAllow: []string{"URL", "PASSWORD", "ORDER", "NAMESPACE"}, RequiredEnv: []string{"URL"}, Overrides: map[string]string{"FILE": "${CONFIG_DIR}/config/cert.pem"}, Files: []ConfigFile{{Kind: "secret", Name: "creds", Key: "cert", Path: "config/cert.pem"}}}}
	f := fakeReader{
		"test/deployment/order": `{"spec":{"template":{"spec":{"containers":[{"name":"app","envFrom":[{"configMapRef":{"name":"config"}},{"secretRef":{"name":"creds"}}],"env":[{"name":"ORDER","value":"explicit"},{"name":"URL","value":"https://$(HOST)/$(PASSWORD)"},{"name":"NAMESPACE","valueFrom":{"fieldRef":{"fieldPath":"metadata.namespace"}}}]}]}}}}`,
		"test/configmap/config": `{"data":{"HOST":"cluster.example","ORDER":"first"}}`,
		"test/secret/creds":     `{"data":{"PASSWORD":"czNjcmV0","ORDER":"c2Vjb25k","cert":"Q0VSVAo="}}`,
	}
	return p, f
}
func TestPrepareKubernetesPrecedenceAllowlistAndFiles(t *testing.T) {
	p, r := configFixture()
	dir := t.TempDir()
	got, err := Prepare(context.Background(), p, r, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Env["ORDER"] != "explicit" || got.Env["URL"] != "https://cluster.example/s3cret" || got.Env["NAMESPACE"] != "test" {
		t.Fatal(got.Env)
	}
	if _, ok := got.Env["HOST"]; ok {
		t.Fatal("non-allowlisted variable escaped")
	}
	if got.Env["FILE"] != filepath.ToSlash(dir)+"/config/cert.pem" {
		t.Fatal(got.Env["FILE"])
	}
	b, _ := os.ReadFile(filepath.Join(dir, "config", "cert.pem"))
	if string(b) != "CERT\n" {
		t.Fatalf("file contents %q", b)
	}
}
func TestPrepareFailsOnMissingSecretAndPodIdentity(t *testing.T) {
	p, r := configFixture()
	delete(r, "test/secret/creds")
	if _, e := Prepare(context.Background(), p, r, t.TempDir()); e == nil {
		t.Fatal("must fail on secret access error")
	}
	p, r = configFixture()
	r["test/deployment/order"] = strings.Replace(r["test/deployment/order"], "metadata.namespace", "status.podIP", 1)
	if _, e := Prepare(context.Background(), p, r, t.TempDir()); e == nil {
		t.Fatal("must not export remote Pod IP")
	}
	p.Source.Overrides["NAMESPACE"] = "local"
	if _, e := Prepare(context.Background(), p, r, t.TempDir()); e != nil {
		t.Fatal(e)
	}
}
func TestKubeExpansionEscapingAndUnknown(t *testing.T) {
	got := expandKube("$$(A):$(A):$(B)", map[string]string{"A": "yes"})
	if got != "$(A):yes:$(B)" {
		t.Fatal(got)
	}
}
func TestPrivatePathTraversal(t *testing.T) {
	for _, path := range []string{"../secret", "a/../../x", "/abs", `C:\secret`, `a\..\secret`, "a//b", "a/./b"} {
		if safeRelative(path) {
			t.Fatal(path)
		}
	}
}
func TestRedactionAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	redactStream(&out, strings.NewReader("password=s3cret\nCERT\n"), []string{"s3cret", "CERT\n"})
	if strings.Contains(out.String(), "s3cret") || strings.Contains(out.String(), "CERT") {
		t.Fatal(out.String())
	}
}
func TestEnvironmentDoesNotInheritSpringOverrides(t *testing.T) {
	t.Setenv("SPRING_APPLICATION_JSON", `{"secret":"bad"}`)
	p := Prepared{Env: map[string]string{"SERVER_PORT": "8080"}}
	for _, e := range appEnvironment(p) {
		if strings.HasPrefix(e, "SPRING_APPLICATION_JSON=") {
			t.Fatal("inherited ambient Spring override")
		}
	}
}
