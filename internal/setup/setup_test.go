package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfirmWritesTheProfileAndRefusesAnImage(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir)
	file, err := ParseLine("python -m unittest tests.test_retry", "python")
	if err != nil {
		t.Fatal(err)
	}
	file.Repo = "/tmp/payrepo"
	if err := Save(path, file); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profile != "python" || len(loaded.Argv) != 4 || loaded.Argv[0] != "python" {
		t.Fatalf("loaded %+v", loaded)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "image") {
		t.Fatalf("saved file names an image: %s", body)
	}
	if err := os.WriteFile(filepath.Join(dir, "setup.json"), []byte(`{"profile":"python","argv":["python"],"image":"agt-sandbox:python"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("image error %v", err)
	}
}

func TestParseLineKeepsTheProfileExplicit(t *testing.T) {
	file, err := ParseLine(`go -- go test ./...`, "")
	if err != nil {
		t.Fatal(err)
	}
	if file.Profile != "go" || strings.Join(file.Argv, " ") != "go test ./..." {
		t.Fatalf("%+v", file)
	}
	if _, err := ParseLine("go test ./...", ""); err == nil {
		t.Fatal("an unnamed profile was accepted")
	}
}
