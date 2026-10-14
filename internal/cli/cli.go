package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

	"errors"

	"github.com/csy20/agt/internal/auth"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/usage"
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
  agt run status RUN
  agt run attach RUN
  agt run diff RUN
  agt run export RUN --output FILE
  agt run apply RUN --digest DIGEST
  agt approve RUN COMMAND --args-hash HASH
  agt version

--profile is required. --repo does not select a language or a test command.
agt profile detect prints a suggestion and does not start a run.
--provider defaults to AGT_PROVIDER. There is no silent provider.
The fake provider is a labelled scripted demo, not a live model.
The control plane is agtd. agt starts it when that binary sits beside agt and nothing is listening.
agt sends the local token from AGT_TOKEN or $AGT_DATA_DIR/auth.token on every route except health.
agt run diff and agt run export review the snapshot. agt run apply writes the source repository only after --digest matches that artifact. Leaving attach does not apply the change.
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
	return attach(base, created.RunID)
}

func runCmd(args []string) int {
	if len(args) == 0 {
		usageText()
		return 2
	}
	switch args[0] {
	case "create":
		return create(args[1:])
	case "status":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: agt run status RUN")
			return 2
		}
		base, err := ensureServer("")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return printStatus(base, args[1])
	case "attach":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "usage: agt run attach RUN")
			return 2
		}
		base, err := ensureServer("")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return attach(base, args[1])
	case "diff":
		return diffCmd(args[1:])
	case "export":
		return exportCmd(args[1:])
	case "apply":
		return applyCmd(args[1:])
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

func printStatus(base, runID string) int {
	var view map[string]any
	if err := getJSON(base+"/v1/runs/"+runID, &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("state %v\n", view["state"])
	if n, ok := asInt64(view["usage_micros"]); ok {
		fmt.Printf("usage %s\n", usage.USD(n))
	}
	raw, _ := json.MarshalIndent(view, "", "  ")
	fmt.Println(string(raw))
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

func attach(base, runID string) int {
	req, err := http.NewRequest(http.MethodGet, base+"/v1/runs/"+runID+"/events?stream=1&after=0", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := setAuth(req); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "events: %s\n", strings.TrimSpace(string(body)))
		return 1
	}
	if err := readSSE(resp.Body, func(event string, data []byte) {
		if line := formatEvent(event, data); line != "" {
			fmt.Println(line)
		}
	}); err != nil && !errorsIsEOF(err) {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	var view struct {
		State       string          `json:"state"`
		SpentMicros int64           `json:"spent_micros"`
		Worktree    string          `json:"worktree"`
		Error       string          `json:"error"`
		Result      json.RawMessage `json:"result"`
	}
	if err := getJSON(base+"/v1/runs/"+runID, &view); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(view.Result) > 0 && string(view.Result) != "null" {
		var result struct {
			Summary string `json:"summary"`
			Diff    string `json:"diff"`
			Tests   struct {
				Passed bool   `json:"passed"`
				Output string `json:"output"`
			} `json:"tests"`
		}
		_ = json.Unmarshal(view.Result, &result)
		fmt.Printf("\n%s\ncost %s\n", result.Summary, usage.USD(view.SpentMicros))
		if result.Diff != "" {
			fmt.Printf("\n%s", result.Diff)
			if !strings.HasSuffix(result.Diff, "\n") {
				fmt.Println()
			}
		}
	} else {
		fmt.Printf("state %s\ncost %s\n", view.State, usage.USD(view.SpentMicros))
	}
	if view.Worktree != "" {
		fmt.Printf("worktree %s\n", view.Worktree)
	}
	switch view.State {
	case "SUCCEEDED":
		return 0
	case "AWAITING_APPROVAL":
		fmt.Fprintln(os.Stderr, "approval required: agt approve "+runID+" COMMAND --args-hash HASH")
		return 3
	case "PAUSED_BUDGET":
		return 4
	default:
		if view.Error != "" {
			fmt.Fprintln(os.Stderr, view.Error)
		}
		return 1
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
		return fmt.Sprintf("tool %v exit %v", payload["tool"], payload["exit_code"])
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
	default:
		return ""
	}
}

func readSSE(r io.Reader, fn func(event string, data []byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 2<<20)
	var event string
	var data bytes.Buffer
	flush := func() {
		if event == "" && data.Len() == 0 {
			return
		}
		name := event
		if name == "" {
			name = "message"
		}
		fn(name, append([]byte(nil), data.Bytes()...))
		event = ""
		data.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}

func ensureServer(explicit string) (string, error) {
	base := explicit
	if base == "" {
		base = os.Getenv("AGT_URL")
	}
	if base == "" {
		base = "http://127.0.0.1:8787"
	}
	base = strings.TrimRight(base, "/")
	if health(base) == nil {
		return base, nil
	}
	if explicit != "" || os.Getenv("AGT_URL") != "" {
		return "", fmt.Errorf("agtd is not healthy at %s", base)
	}
	bin, err := findDaemonBinary()
	if err != nil {
		return "", fmt.Errorf("agtd is not running at %s: %w", base, err)
	}
	cmd, logPath, err := startDaemon(bin)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if health(base) == nil {
			fmt.Fprintf(os.Stderr, "started agtd pid %d (log %s)\n", cmd.Process.Pid, logPath)
			return base, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("agtd pid %d did not become healthy (log %s)", cmd.Process.Pid, logPath)
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

func health(base string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health %d", resp.StatusCode)
	}
	return nil
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

func errorsIsEOF(err error) bool {
	return err == io.EOF
}
