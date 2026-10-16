package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("AGT_CODEX_STUB") != "" {
		runCodexStub()
		return
	}
	os.Exit(m.Run())
}

func TestAskRefusesMutationAndUnauthorizedBeforeLaunch(t *testing.T) {
	work := t.TempDir()
	if _, err := Ask(context.Background(), AskRequest{Command: []string{"/bin/false"}, WorkDir: work, Prompt: "explain", Mutating: true, Authorize: true}); !errors.Is(err, ErrMutationUnavailable) {
		t.Fatalf("mutating: %v", err)
	}
	if _, err := Ask(context.Background(), AskRequest{Command: []string{"/bin/false"}, WorkDir: work, Prompt: "explain"}); !errors.Is(err, ErrLiveUnavailable) {
		t.Fatalf("unauthorized: %v", err)
	}
}

func TestAskBlocksWhenReadOnlyCanReadOutside(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "boundary")
	t.Setenv("AGT_STUB_LOG", logPath)
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("def charge():\n    return 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const prompt = "explain-charge-marker"
	res, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    prompt,
		Authorize: true,
	})
	var boundary *BoundaryError
	if !errors.As(err, &boundary) || !boundary.OutsideRead || boundary.TurnStarted || boundary.WriteAllowed {
		t.Fatalf("boundary err=%v res=%+v", err, res)
	}
	if res.TurnStarted || res.PromptSent || res.Live || res.Label != "blocked" || !res.FilesUnchanged {
		t.Fatalf("result %+v", res)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, prompt) || strings.Contains(text, "thread/start") || strings.Contains(text, "turn/start") {
		t.Fatalf("prompt or turn was sent:\n%s", text)
	}
	if !strings.Contains(text, "command/exec") || !strings.Contains(text, `"permissionProfile":"agt-snapshot"`) {
		t.Fatalf("preflight was not sent:\n%s", text)
	}
	if strings.Contains(text, "sandboxPolicy") || strings.Contains(text, "--sandbox") {
		t.Fatalf("legacy sandbox settings were sent:\n%s", text)
	}
	if !strings.Contains(text, "--disable") || !strings.Contains(text, "shell_tool") || !strings.Contains(text, `default_permissions="agt-snapshot"`) {
		t.Fatalf("profile flags were not used:\n%s", text)
	}
}

func TestAskStreamsDeclinesAndCompletesOnAStub(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "stream")
	t.Setenv("AGT_STUB_LOG", logPath)
	work := t.TempDir()
	body := []byte("def charge():\n    return amount + amount\n")
	if err := os.WriteFile(work+"/charge.py", body, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain the charge method",
		Authorize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Live || res.Label != LabelScripted || res.Outcome != string(OutcomeCompleted) || res.Text != "charge adds the amount twice" {
		t.Fatalf("result %+v", res)
	}
	if !res.PromptSent || !res.TurnStarted || !res.FilesUnchanged || res.Backend != "stub-codex" || res.CostKnown {
		t.Fatalf("result %+v", res)
	}
	if res.RuntimeVersion != "" || res.RuntimeDigest == "" || res.Limitation != promptOnlyLimitation {
		t.Fatalf("provenance %+v", res)
	}
	if strings.Join(res.Denied, ",") != "item/commandExecution/requestApproval,item/fileChange/requestApproval,item/permissions/requestApproval" {
		t.Fatalf("denied %+v", res.Denied)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"decision":"accept"`) || strings.Contains(string(raw), "acceptForSession") {
		t.Fatalf("approval was accepted:\n%s", raw)
	}
	got, err := os.ReadFile(work + "/charge.py")
	if err != nil || string(got) != string(body) {
		t.Fatalf("snapshot changed %q %v", got, err)
	}
}

func TestAskInterruptOnCancel(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "cancel")
	t.Setenv("AGT_STUB_LOG", logPath)
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			raw, readErr := os.ReadFile(logPath)
			if readErr == nil && strings.Contains(string(raw), "waiting-interrupt") {
				time.Sleep(100 * time.Millisecond)
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	res, err := Ask(ctx, AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain the charge method",
		Authorize: true,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v result %+v", err, res)
	}
	if res.Live || res.Outcome != string(OutcomeCancelled) {
		t.Fatalf("result %+v", res)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "turn/interrupt") {
		t.Fatalf("interrupt was not sent:\n%s", raw)
	}
}

func TestAskInterruptsACommandThatRanWithoutApproval(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "breach")
	t.Setenv("AGT_STUB_LOG", logPath)
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain the charge method",
		Authorize: true,
	})
	if !errors.Is(err, ErrBoundary) || res.Live {
		t.Fatalf("err %v result %+v", err, res)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "turn/interrupt") {
		t.Fatalf("interrupt was not sent:\n%s", raw)
	}
}

func TestAskProtocolErrorDoesNotSendAPrompt(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "protocol")
	t.Setenv("AGT_STUB_LOG", logPath)
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const prompt = "explain-charge-marker"
	res, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    prompt,
		Authorize: true,
	})
	if !errors.Is(err, ErrProtocolMismatch) && !errors.Is(err, ErrBoundary) {
		t.Fatalf("err %v", err)
	}
	if res.PromptSent || res.TurnStarted || res.Live {
		t.Fatalf("result %+v", res)
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), prompt) || strings.Contains(string(raw), "turn/start") {
		t.Fatalf("prompt sent:\n%s", raw)
	}
}

func TestAskDoesNotPassAPIKeysToCodex(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "env")
	t.Setenv("AGT_STUB_LOG", logPath)
	t.Setenv("OPENAI_API_KEY", "sk-test-secret-value")
	t.Setenv("CODEX_API_KEY", "sk-test-secret-value")
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain",
		Authorize: true,
	})
	if err == nil {
		t.Fatal("expected the env stub to stop before a turn")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-test-secret-value") || strings.Contains(string(raw), "OPENAI_API_KEY") || strings.Contains(string(raw), "CODEX_API_KEY") {
		t.Fatal("child environment contained an API key")
	}
}

func TestAskBlocksWhenShellToolStaysEnabled(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "tools")
	t.Setenv("AGT_STUB_LOG", logPath)
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain-charge-marker",
		Authorize: true,
	})
	var boundary *BoundaryError
	if !errors.As(err, &boundary) || boundary.TurnStarted || res.PromptSent || res.TurnStarted || res.Live {
		t.Fatalf("err %v result %+v", err, res)
	}
	if !strings.Contains(err.Error(), "shell_tool") || !strings.Contains(err.Error(), "turn/start was not sent") {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "explain-charge-marker") || strings.Contains(string(raw), "turn/start") || strings.Contains(string(raw), "command/exec") {
		t.Fatalf("tool gate did not stop the turn:\n%s", raw)
	}
}

func TestAskExecutableBasenameIsNotLive(t *testing.T) {
	logPath := t.TempDir() + "/stub.log"
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "stream")
	t.Setenv("AGT_STUB_LOG", logPath)
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	if err := copyFile(bin, os.Args[0]); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Ask(context.Background(), AskRequest{
		Command:   []string{bin, "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain the charge method",
		Authorize: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Live || res.Label != LabelScripted || res.Backend != "stub-codex" || res.RuntimeVersion != "" || res.Outcome != string(OutcomeCompleted) {
		t.Fatalf("result %+v", res)
	}
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func TestAskReportsChatGPTLoginRequirement(t *testing.T) {
	t.Setenv("AGT_CODEX_STUB", "1")
	t.Setenv("AGT_STUB_MODE", "logged-out")
	work := t.TempDir()
	if err := os.WriteFile(work+"/charge.py", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Ask(context.Background(), AskRequest{
		Command:   []string{os.Args[0], "-test.run=^$"},
		WorkDir:   work,
		Prompt:    "explain",
		Authorize: true,
	})
	var login *LoginError
	if !errors.As(err, &login) || !errors.Is(err, ErrLogin) {
		t.Fatalf("err %v", err)
	}
	if !strings.Contains(err.Error(), "codex login") || strings.Contains(err.Error(), "sk-") {
		t.Fatal(err)
	}
}

func runCodexStub() {
	joined := strings.Join(os.Args, " ")
	if strings.Contains(joined, "login") {
		if os.Getenv("AGT_STUB_MODE") == "logged-out" {
			fmt.Println("Not logged in")
			return
		}
		fmt.Println("Logged in using ChatGPT")
		return
	}
	if os.Getenv("AGT_STUB_MODE") == "env" {
		appendLog(strings.Join(os.Environ(), "\n"))
		return
	}
	if strings.Contains(joined, "--version") {
		fmt.Println("stub-codex 0")
		return
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), maxCodexLine)
	mode := os.Getenv("AGT_STUB_MODE")
	appendLog("argv " + strings.Join(os.Args, " "))
	for in.Scan() {
		line := append([]byte(nil), in.Bytes()...)
		appendLog(string(line))
		var msg map[string]any
		if json.Unmarshal(line, &msg) != nil {
			return
		}
		method, _ := msg["method"].(string)
		switch method {
		case "initialize":
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{"userAgent": "stub-codex"}})
			if mode == "protocol" {
				_, _ = os.Stdout.WriteString("{")
				return
			}
		case "config/read":
			shell := false
			if mode == "tools" {
				shell = true
			}
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{
				"config": map[string]any{
					"sandbox_mode":        nil,
					"default_permissions": codexProfile,
					"permissions":         map[string]any{codexProfile: map[string]any{}},
					"features":            map[string]any{"shell_tool": shell},
					"mcp_servers":         map[string]any{"node_repl": map[string]any{"enabled": false}},
				},
				"origins": map[string]any{},
			}})
		case "command/exec":
			argv := stubArgv(msg)
			name := ""
			if len(argv) > 0 {
				name = argv[0]
			}
			exitCode := 1
			stdout := ""
			joinedArgv := strings.Join(argv, " ")
			if name == "cat" && (mode == "boundary" || strings.Contains(joinedArgv, codexAllowedName)) {
				exitCode = 0
				stdout = codexCanaryText + "\n"
			}
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{"exitCode": exitCode, "stdout": stdout, "stderr": ""}})
		case "thread/start":
			cwd := ""
			if params, ok := msg["params"].(map[string]any); ok {
				cwd, _ = params["cwd"].(string)
			}
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{
				"thread":                  map[string]any{"id": "thr_stub"},
				"activePermissionProfile": map[string]any{"id": codexProfile},
				"runtimeWorkspaceRoots":   []string{cwd},
				"sandbox":                 map[string]any{"type": "readOnly", "networkAccess": false},
			}})
		case "turn/start":
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{"turn": map[string]any{"id": "turn_stub", "status": "inProgress", "items": []any{}}}})
			switch mode {
			case "stream":
				stubDeltas()
				stubApproval(in, "item/commandExecution/requestApproval", 100)
				stubApproval(in, "item/fileChange/requestApproval", 101)
				stubApproval(in, "item/permissions/requestApproval", 102)
				stubCompleted("completed")
				return
			case "cancel":
				appendLog("waiting-interrupt")
				stubWaitInterrupt(in)
				return
			case "breach":
				stubWrite(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"type": "commandExecution", "id": "c1", "status": "completed", "command": "cat"}}})
				stubWaitInterrupt(in)
				return
			}
		case "turn/interrupt":
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{}})
			stubCompleted("interrupted")
			return
		}
	}
}

func stubArgv(msg map[string]any) []string {
	params, _ := msg["params"].(map[string]any)
	raw, _ := params["command"].([]any)
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}

func stubDeltas() {
	stubWrite(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"delta": "charge adds "}})
	stubWrite(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"delta": "the amount twice"}})
}

func stubApproval(in *bufio.Scanner, method string, id int) {
	stubWrite(map[string]any{"method": method, "id": id, "params": map[string]any{"itemId": "item", "threadId": "thr_stub", "turnId": "turn_stub"}})
	if in.Scan() {
		appendLog(in.Text())
	}
}

func stubWaitInterrupt(in *bufio.Scanner) {
	for in.Scan() {
		appendLog(in.Text())
		var msg map[string]any
		if json.Unmarshal(in.Bytes(), &msg) != nil {
			return
		}
		if msg["method"] == "turn/interrupt" {
			stubWrite(map[string]any{"id": msg["id"], "result": map[string]any{}})
			stubCompleted("interrupted")
			return
		}
	}
}

func stubCompleted(status string) {
	stubWrite(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "thr_stub", "turn": map[string]any{"id": "turn_stub", "status": status, "items": []any{}}}})
}

func stubWrite(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	_, _ = os.Stdout.Write(append(raw, '\n'))
}

func appendLog(line string) {
	path := os.Getenv("AGT_STUB_LOG")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, line)
}
