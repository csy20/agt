package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/csy20/agt/internal/auth"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/version"
)

func Run(args []string) int {
	if len(args) < 2 {
		usageText()
		return 2
	}
	switch args[1] {
	case "version", "--version", "-v":
		fmt.Println(version.Product)
		return 0
	case "fix":
		return fix(args[2:])
	case "profile":
		return profileCommand(args[2:])
	case "approve":
		return approve(args[2:])
	case "run":
		return runCmd(args[2:])
	case "doctor":
		return doctor(args[2:])
	case "completion":
		return completion(args[2:])
	case "help", "-h", "--help":
		usageText()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[1])
		usageText()
		return 2
	}
}

func usageText() {
	fmt.Fprint(os.Stderr, `agt is a durable coding-agent terminal.

  agt fix --repo PATH --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --provider fake --script FILE "goal"
  agt fix --repo PATH --profile go --test-arg go --test-arg test --test-arg ./... "goal"
  agt profile detect --repo PATH
  agt run create --goal TEXT --repo PATH --profile python --test-arg python --test-arg -m --test-arg unittest --test-arg tests.test_retry --provider fake --script FILE
  agt run list [--limit N] [--json]
  agt run status RUN [--json]
  agt run attach RUN [--json]
  agt run diff RUN
  agt run export RUN --output FILE
  agt run apply RUN --digest DIGEST
  agt run pause RUN
  agt run resume RUN
  agt run budget-resume RUN [--budget-cents N]
  agt run cancel RUN
  agt run cleanup
  agt approve RUN COMMAND --args-hash HASH
  agt doctor [--json]
  agt completion bash|zsh|fish|powershell
  agt version

--profile is required. --repo does not select a language or a test command.
agt profile detect prints a suggestion and does not start a run.
--provider defaults to AGT_PROVIDER. --model checks AGT_MODEL_NAME on agtd and does not call a hosted model.
The fake provider is a labelled scripted demo, not a live model.
The control plane is agtd. agt starts it when that binary sits beside agt and the daemon identity matches this client.
agt sends the local token from AGT_TOKEN or $AGT_DATA_DIR/auth.token. GET /healthz and GET /v1/identity do not use the token.
agt run diff and agt run export review the snapshot. agt run apply writes the source repository only after --digest matches that artifact.
Leaving agt run attach, including Ctrl+C, does not cancel the run and does not apply the change. agt run cancel does.
A dollar cap is not enforced when model prices are zero or the provider reports no usage.
Set NO_COLOR to keep the progress lines plain. --json prints machine-readable output.
agt doctor names the next command when Docker, Postgres, the token, the listen address, the images, or the daemon version fail.
`)
}

func fix(args []string) int {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repoFlag := fs.String("repo", "", "repository to copy into a run worktree")
	providerFlag := fs.String("provider", "", "fake or openai; defaults to AGT_PROVIDER")
	scriptFlag := fs.String("script", "", "fake-provider script JSON")
	budget := fs.Int("budget-cents", 100, "run budget in cents")
	server := fs.String("server", "", "agtd base URL")
	modelFlag := fs.String("model", "", "require this AGT_MODEL_NAME on agtd")
	asJSON := fs.Bool("json", false, "print events as JSON")
	pf := addProfileFlags(fs)
	if err := fs.Parse(reorderFlags(args, runValueFlags(true))); err != nil {
		return 2
	}
	goal := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if goal == "" || *repoFlag == "" {
		fmt.Fprintln(os.Stderr, "fix requires a goal and --repo")
		return 2
	}
	prof, err := pf.request()
	if err != nil {
		if errors.Is(err, errProfileRequired) {
			fmt.Fprintln(os.Stderr, "fix requires --profile. Passing --repo does not select a language or a test command.")
			printSuggestion(*repoFlag)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 2
	}
	provider, repo, script, err := runInputs(*providerFlag, *repoFlag, *scriptFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := matchModel(base, *modelFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	body := map[string]any{
		"goal": goal, "repo_path": repo, "provider": provider, "budget_cents": *budget,
		"profile": profilePayload(prof),
	}
	if script != "" {
		body["script_path"] = script
	}
	var created struct {
		RunID string `json:"run_id"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := postJSON(base+"/v1/runs", body, &created); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if created.Error != "" {
		fmt.Fprintln(os.Stderr, created.Error)
		return 1
	}
	fmt.Printf("run %s\n", created.RunID)
	return attach(base, created.RunID, *asJSON)
}

func runCmd(args []string) int {
	if len(args) == 0 {
		usageText()
		return 2
	}
	switch args[0] {
	case "create":
		return create(args[1:])
	case "list":
		return listCmd(args[1:])
	case "status":
		return statusCmd(args[1:])
	case "attach":
		return attachCmd(args[1:])
	case "diff":
		return diffCmd(args[1:])
	case "export":
		return exportCmd(args[1:])
	case "apply":
		return applyCmd(args[1:])
	case "pause":
		return lifecycleCmd("pause", args[1:])
	case "resume":
		return lifecycleCmd("resume", args[1:])
	case "budget-resume":
		return budgetResumeCmd(args[1:])
	case "cancel":
		return lifecycleCmd("cancel", args[1:])
	case "cleanup":
		return lifecycleCmd("cleanup", args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown run command %q\n", args[0])
		return 2
	}
}

func create(args []string) int {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	goal := fs.String("goal", "", "run goal")
	repoFlag := fs.String("repo", "", "repository path")
	providerFlag := fs.String("provider", "", "fake or openai; defaults to AGT_PROVIDER")
	scriptFlag := fs.String("script", "", "fake-provider script")
	server := fs.String("server", "", "agtd base URL")
	modelFlag := fs.String("model", "", "require this AGT_MODEL_NAME on agtd")
	pf := addProfileFlags(fs)
	if err := fs.Parse(reorderFlags(args, runValueFlags(false))); err != nil {
		return 2
	}
	if *goal == "" || *repoFlag == "" {
		fmt.Fprintln(os.Stderr, "create requires --goal and --repo")
		return 2
	}
	prof, err := pf.request()
	if err != nil {
		if errors.Is(err, errProfileRequired) {
			fmt.Fprintln(os.Stderr, "create requires --profile. Passing --repo does not select a language or a test command.")
			printSuggestion(*repoFlag)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 2
	}
	provider, repo, script, err := runInputs(*providerFlag, *repoFlag, *scriptFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := matchModel(base, *modelFlag); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	body := map[string]any{
		"goal": *goal, "repo_path": repo, "provider": provider,
		"profile": profilePayload(prof),
	}
	if script != "" {
		body["script_path"] = script
	}
	var created struct {
		RunID string `json:"run_id"`
		State string `json:"state"`
		Error string `json:"error"`
	}
	if err := postJSON(base+"/v1/runs", body, &created); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if created.Error != "" {
		fmt.Fprintln(os.Stderr, created.Error)
		return 1
	}
	fmt.Printf("%s %s\n", created.RunID, created.State)
	return 0
}

func approve(args []string) int {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	hash := fs.String("args-hash", "", "hash of the canonical command")
	yes := fs.Bool("yes", false, "approve the command currently stored for this id")
	decision := fs.String("decision", "approved", "approved or rejected")
	server := fs.String("server", "", "agtd base URL")
	if err := fs.Parse(reorderFlags(args, map[string]bool{
		"args-hash": true, "decision": true, "server": true,
	})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 2 {
		fmt.Fprintln(os.Stderr, "usage: agt approve RUN COMMAND --args-hash HASH")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *hash == "" {
		if !*yes {
			fmt.Fprintln(os.Stderr, "pass --args-hash to bind the approval, or --yes to approve the stored command")
			return 2
		}
		var cmd struct {
			ArgsHash  string          `json:"args_hash"`
			Canonical json.RawMessage `json:"canonical_args"`
			Tool      string          `json:"tool"`
		}
		if err := getJSON(base+"/v1/runs/"+rest[0]+"/commands/"+rest[1], &cmd); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		*hash = cmd.ArgsHash
		fmt.Printf("tool %s\n%s\n", cmd.Tool, cmd.Canonical)
	}
	var view struct {
		State string `json:"state"`
		Error string `json:"error"`
	}
	err = postJSON(base+"/v1/runs/"+rest[0]+"/approvals", map[string]any{
		"command_id": rest[1], "args_hash": *hash, "decision": *decision, "approver": "local",
	}, &view)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if view.Error != "" {
		fmt.Fprintln(os.Stderr, view.Error)
		return 1
	}
	fmt.Printf("%s %s\n", rest[0], view.State)
	return 0
}

func asInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func formatEvent(event string, data []byte) string {
	var payload map[string]any
	_ = json.Unmarshal(data, &payload)
	switch event {
	case "state.changed":
		return fmt.Sprintf("state %v -> %v", payload["from"], payload["to"])
	case "plan.updated":
		return "plan updated"
	case "command.finished":
		line := fmt.Sprintf("tool %v exit %v", payload["tool"], payload["exit_code"])
		if excerpt := oneLine(payload["output_excerpt"], 120); excerpt != "" {
			line += " " + excerpt
		}
		return line
	case "command.started":
		return fmt.Sprintf("tool %v started", payload["tool"])
	case "approval.requested":
		return fmt.Sprintf("approval required %v %v hash %v", payload["tool"], payload["command_id"], payload["args_hash"])
	case "approval.recorded":
		return fmt.Sprintf("approval %v", payload["decision"])
	case "budget.paused":
		return "paused: budget"
	case "run.succeeded":
		return "succeeded"
	case "run.failed":
		return fmt.Sprintf("failed: %v", payload["reason"])
	case "workspace.captured":
		return fmt.Sprintf("workspace captured %v %v", payload["strategy"], payload["revision"])
	case "patch.applied":
		return fmt.Sprintf("applied %v files digest %v", payload["files"], payload["digest"])
	case "verification.failed":
		return fmt.Sprintf("verification failed exit %v outcome %v", payload["exit_code"], payload["outcome"])
	case "baseline.recorded":
		return fmt.Sprintf("baseline exit %v", payload["exit_code"])
	default:
		return ""
	}
}

func oneLine(v any, n int) string {
	if v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" || s == "<nil>" {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		s = s[:n] + "..."
	}
	return s
}

func startDaemon(bin string) (*exec.Cmd, string, error) {
	dataDir := config.Load().DataDir
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create data dir %s: %w", dataDir, err)
	}
	logPath := filepath.Join(dataDir, "agtd.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, "", fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	cmd := exec.Command(bin)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachDaemon(cmd)
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return nil, "", fmt.Errorf("start %s: %w", bin, err)
	}
	logFile.Close()
	return cmd, logPath, nil
}

func runInputs(providerFlag, repoFlag, scriptFlag string) (provider, repo, script string, err error) {
	provider, err = resolveProvider(providerFlag)
	if err != nil {
		return "", "", "", err
	}
	repo, err = absolutePath(repoFlag)
	if err != nil {
		return "", "", "", err
	}
	script, err = prepareFakeScript(provider, scriptFlag)
	if err != nil {
		return "", "", "", err
	}
	return provider, repo, script, nil
}

func postJSON(url string, body any, dest any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := setAuth(req); err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if resp.StatusCode >= 300 {
		var wrapped struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &wrapped)
		if wrapped.Error != "" {
			return fmt.Errorf("%s", wrapped.Error)
		}
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func getJSON(url string, dest any) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if err := setAuth(req); err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return json.Unmarshal(data, dest)
}

func setAuth(req *http.Request) error {
	dir := config.Load().DataDir
	token, err := auth.Read(dir)
	if err != nil {
		return fmt.Errorf("local API token: %w. Set the same AGT_DATA_DIR or AGT_TOKEN as agtd", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}
