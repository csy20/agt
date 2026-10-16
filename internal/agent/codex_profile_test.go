package agent

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileArgsGrantOnlyTheSnapshotAndRuntime(t *testing.T) {
	work := t.TempDir()
	runtime := t.TempDir()
	args, err := profileArgs(work, runtime)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(args, "\n")
	for _, want := range []string{
		"--disable",
		"shell_tool",
		"plugins",
		"browser_use",
		"computer_use",
		"web_search_request",
		"apps",
		`default_permissions="agt-snapshot"`,
		"network.enabled=false",
		"mcp_servers.node_repl.enabled=false",
		work,
		runtime,
		`":minimal"="read"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s\n%s", want, text)
		}
	}
	if strings.Contains(text, "--sandbox") || strings.Contains(text, "sandbox_mode") || strings.Contains(text, ":danger-full-access") || strings.Contains(text, ":workspace") {
		t.Fatal(text)
	}
	if _, err := profileArgs(work+`"`, ""); err == nil {
		t.Fatal("quoted path was accepted")
	}
}

func TestCodexRuntimeRootFindsTheVendorPrefix(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "lib", "node_modules", "@openai", "codex", "bin")
	vendorBin := filepath.Join(root, "lib", "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-linux-x64", "vendor", "x86_64-unknown-linux-musl", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(vendorBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex.js"), []byte("#!/usr/bin/env node\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendorBin, "codex"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "codex")
	if err := os.Symlink(filepath.Join(binDir, "codex.js"), link); err != nil {
		t.Fatal(err)
	}
	got := codexRuntimeRoot(link)
	want := filepath.Join(root, "lib", "node_modules", "@openai", "codex", "node_modules", "@openai", "codex-linux-x64", "vendor", "x86_64-unknown-linux-musl")
	if got != want {
		t.Fatalf("root %s", got)
	}
}

func TestCodexRuntimeRootResolvesBareName(t *testing.T) {
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not installed")
	}
	if codexRuntimeRoot("codex") == "" || codexRuntimeRoot("codex") != codexRuntimeRoot(path) {
		t.Fatalf("bare name root %s path root %s", codexRuntimeRoot("codex"), codexRuntimeRoot(path))
	}
	if !strings.Contains(resolveCodex("codex"), "codex.js") {
		t.Fatalf("resolved %s", resolveCodex("codex"))
	}
}

func TestCodexRuntimeRootFindsInstalledCodex(t *testing.T) {
	path, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex is not installed")
	}
	root := codexRuntimeRoot(path)
	if root == "" {
		t.Fatal("vendor prefix not found")
	}
	info, err := os.Stat(filepath.Join(root, "bin", "codex"))
	if err != nil || info.IsDir() {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err == nil && root == home {
		t.Fatal("vendor root is the home directory")
	}
}

func TestToolsDisabledRejectsAnOpenShell(t *testing.T) {
	good := []byte(`{"config":{"sandbox_mode":null,"default_permissions":"agt-snapshot","permissions":{"agt-snapshot":{}},"features":{"shell_tool":false},"mcp_servers":{"node_repl":{"enabled":false}}}}`)
	if err := toolsDisabled(good); err != nil {
		t.Fatal(err)
	}
	open := []byte(`{"config":{"sandbox_mode":null,"default_permissions":"agt-snapshot","permissions":{"agt-snapshot":{}},"features":{"shell_tool":true}}}`)
	if err := toolsDisabled(open); err == nil || !strings.Contains(err.Error(), "shell_tool") {
		t.Fatal(err)
	}
	legacy := []byte(`{"config":{"sandbox_mode":"read-only","default_permissions":"agt-snapshot","permissions":{"agt-snapshot":{}},"features":{"shell_tool":false}}}`)
	if err := toolsDisabled(legacy); err == nil || !strings.Contains(err.Error(), "sandbox_mode") {
		t.Fatal(err)
	}
}

func TestTaggedPromptKeepsRelativeExcerpts(t *testing.T) {
	work := t.TempDir()
	if err := os.MkdirAll(filepath.Join(work, "payments"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("def charge(amount):\n    return amount + amount\n")
	if err := os.WriteFile(filepath.Join(work, "payments", "processor.py"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := taggedPrompt(work, "explain the charge method", []string{"payments/processor.py", "tests/test_retry.py", "../secret"})
	if err == nil {
		t.Fatal("traversal excerpt was accepted")
	}
	got, err = taggedPrompt(work, "explain the charge method", []string{"payments/processor.py", "tests/test_retry.py"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "def charge") || !strings.Contains(got, "Agent tools are disabled") || strings.Contains(got, "test_retry") {
		t.Fatalf("prompt %s", got)
	}
	if _, err := taggedPrompt(work, "explain", []string{"/etc/passwd"}); err == nil {
		t.Fatal("absolute excerpt was accepted")
	}
}

func TestRootsContainMatchesTheSnapshot(t *testing.T) {
	if !rootsContain([]string{"/tmp/other", "/tmp/snap"}, "/tmp/snap") {
		t.Fatal("missing root")
	}
	if rootsContain([]string{"/tmp/snap/nested"}, "/tmp/snap") {
		t.Fatal("child root matched the parent")
	}
}

func TestCodexExcerptsJSONShape(t *testing.T) {
	raw := json.RawMessage(`{"config":{"sandbox_mode":null,"default_permissions":"agt-snapshot","permissions":{"agt-snapshot":{"filesystem":{}}},"features":{"shell_tool":false}}}`)
	if err := toolsDisabled(raw); err != nil {
		t.Fatal(err)
	}
}
