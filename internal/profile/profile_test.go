package profile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testImages() Images {
	return Images{Python: "agt-sandbox:python", Go: "agt-sandbox:go", Node: "agt-sandbox:node"}
}

func TestResolveFillsDaemonFields(t *testing.T) {
	got, err := Resolve(Request{
		Name: NamePython,
		Argv: []string{"python", "-m", "unittest", "tests.test_retry"},
	}, testImages(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Contract != Contract || got.Name != NamePython || got.Image != "agt-sandbox:python" {
		t.Fatalf("identity %+v", got.Identity())
	}
	if got.Arch != "linux/amd64" || got.Network != "none" || got.Deps != "prebuilt" || len(got.Prepare) != 0 {
		t.Fatalf("contract %+v", got)
	}
	if got.TimeoutSec != 60 || got.Memory != "256m" || got.CPUs != "1" || got.PIDs != 64 || got.Workdir != "." {
		t.Fatalf("limits %+v", got)
	}
	if strings.Join(got.Env, ",") != strings.Join(EnvFor(NamePython), ",") {
		t.Fatalf("env %q", got.Env)
	}
	joined := strings.Join(got.Env, "\n")
	for _, secret := range []string{"AGT_", "AWS_", "DATABASE_URL", "SSH_AUTH_SOCK"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("env contains %s: %s", secret, joined)
		}
	}
	argv := got.ContainerArgv()
	if strings.Join(argv, " ") != "timeout -s KILL 60 python -m unittest tests.test_retry" {
		t.Fatalf("argv %q", argv)
	}
	raw, err := got.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Image != got.Image || parsed.Name != got.Name {
		t.Fatalf("parsed %+v", parsed)
	}
	if _, err := Parse([]byte(`{}`)); err == nil {
		t.Fatal("empty profile was accepted")
	}
}

func TestTypeScriptIsTheJavaScriptProfile(t *testing.T) {
	got, err := Resolve(Request{
		Name: NameTypeScript,
		Argv: []string{"node", "--test"},
	}, testImages(), "arm64")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != NameJavaScript || got.Alias != NameTypeScript || got.Image != "agt-sandbox:node" {
		t.Fatalf("%+v", got)
	}
	if got.Arch != "linux/arm64" {
		t.Fatalf("arch %s", got.Arch)
	}
}

func TestRequestRejectsShellPrepareAndLimits(t *testing.T) {
	images := testImages()
	cases := []Request{
		{Name: NamePython, Argv: []string{"bash", "-c", "echo hi"}},
		{Name: NamePython, Argv: []string{"sh"}},
		{Name: NameGo, Argv: []string{"/usr/bin/go", "test"}},
		{Name: NamePython, Argv: []string{"python", "a\x00b"}},
		{Name: NamePython},
		{Name: NamePython, Argv: []string{"python"}, Prepare: true},
		{Name: NamePython, Argv: []string{"python"}, TimeoutSec: 301},
		{Name: NamePython, Argv: []string{"python"}, Memory: "1g"},
		{Name: NamePython, Argv: []string{"python"}, PIDs: 8},
		{Name: NamePython, Argv: []string{"python"}, Workdir: "../outside"},
		{Name: "ruby", Argv: []string{"ruby"}},
	}
	for _, req := range cases {
		if _, err := Resolve(req, images, "amd64"); err == nil {
			t.Fatalf("accepted %+v", req)
		}
	}
	long := make([]string, 17)
	for i := range long {
		long[i] = "python"
	}
	if _, err := Resolve(Request{Name: NamePython, Argv: long}, images, "amd64"); err == nil {
		t.Fatal("accepted 17 arguments")
	}
}

func TestHostEnvDropsSecrets(t *testing.T) {
	t.Setenv("AGT_MODEL_API_KEY", "sk-test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "aws-test")
	t.Setenv("AGT_DATABASE_URL", "postgres://agt:secret@127.0.0.1/agt")
	p, err := Resolve(Request{Name: NamePython, Argv: []string{"python3", "-m", "unittest"}}, testImages(), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, item := range HostEnv(p, root) {
		if strings.Contains(item, "sk-test") || strings.Contains(item, "aws-test") || strings.Contains(item, "postgres://agt:secret") {
			t.Fatalf("host env %s", item)
		}
		if strings.HasPrefix(item, "PYTHONPATH=") && item != "PYTHONPATH="+root {
			t.Fatalf("pythonpath %s", item)
		}
	}
}

func TestProposeDoesNotAuthorize(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.py", "print(1)\n")
	got := Propose(root)
	if got.Name != NamePython {
		t.Fatalf("%+v", got)
	}
	write("go.mod", "module example.com/x\n\ngo 1.23.0\n")
	got = Propose(root)
	if got.Name != "" || !strings.Contains(got.Reason, "multiple") {
		t.Fatalf("%+v", got)
	}
	os.Remove(filepath.Join(root, "main.py"))
	os.Remove(filepath.Join(root, "go.mod"))
	write("package.json", `{"name":"demo","private":true}`+"\n")
	write(".agt/profile.json", `{"name":"go","image":"evil"}`+"\n")
	got = Propose(root)
	if got.Name != NameJavaScript {
		t.Fatalf("repo config selected %+v", got)
	}
	os.Remove(filepath.Join(root, "package.json"))
	write(".ssh/go.mod", "module secret\n")
	write("app.py", "print(1)\n")
	got = Propose(root)
	if got.Name != NamePython {
		t.Fatalf("secret dir counted %+v", got)
	}
}
