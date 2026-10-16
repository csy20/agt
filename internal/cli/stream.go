package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/usage"
	"github.com/csy20/agt/internal/verify"
	"github.com/csy20/agt/internal/version"
)

var errDaemonDown = errors.New("agtd is not running")

var errStreamIdle = errors.New("event stream went quiet")

const streamIdle = 45 * time.Second

type daemonIdentity struct {
	Product      string   `json:"product"`
	Version      string   `json:"version"`
	Contract     string   `json:"contract"`
	Build        string   `json:"build"`
	Capabilities []string `json:"capabilities"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	Listen       string   `json:"listen"`
}

func clientBase(explicit string) (string, bool) {
	base := strings.TrimSpace(explicit)
	pinned := base != "" || strings.TrimSpace(os.Getenv("AGT_URL")) != ""
	if base == "" {
		base = strings.TrimSpace(os.Getenv("AGT_URL"))
	}
	if base == "" {
		base = "http://127.0.0.1:8787"
	}
	return strings.TrimRight(base, "/"), pinned
}

func discover(base string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w at %s. Next: start agtd, or place agtd beside agt and retry. `agt doctor` checks Docker, Postgres, the images, the token, and the listen address", errDaemonDown, base)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	product := resp.Header.Get(version.ProductHeader)
	got := resp.Header.Get(version.VersionHeader)
	if product != version.Name || got == "" {
		return fmt.Errorf("HTTP %d from %s is not this agt daemon (missing %s). Next: stop the other process and start agtd from this install, then run `agt doctor`", resp.StatusCode, base, version.ProductHeader)
	}
	if got != version.Product {
		return fmt.Errorf("agtd at %s is version %s and this agt is %s. Next: install matching agt and agtd binaries and restart agtd", base, got, version.Product)
	}
	if missing := version.MissingAdvertised(resp.Header.Get(version.ContractHeader), version.ParseCapabilities(resp.Header.Get(version.CapabilitiesHeader))); len(missing) > 0 {
		return version.IncompatibleDaemon(base, got, missing)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agtd %s at %s is up but /healthz returned %d. Next: run `agt doctor`", got, base, resp.StatusCode)
	}
	return nil
}

func fetchIdentity(base string) (daemonIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/identity", nil)
	if err != nil {
		return daemonIdentity{}, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return daemonIdentity{}, fmt.Errorf("%w at %s. Next: start agtd and run `agt doctor`", errDaemonDown, base)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return daemonIdentity{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return daemonIdentity{}, fmt.Errorf("GET /v1/identity returned %d. Next: restart agtd from this install", resp.StatusCode)
	}
	var id daemonIdentity
	if err := json.Unmarshal(data, &id); err != nil {
		return daemonIdentity{}, fmt.Errorf("agt identity response is not JSON. Next: restart agtd from this install")
	}
	if id.Product != version.Name || id.Version != version.Product {
		return daemonIdentity{}, fmt.Errorf("identity %s %s does not match this agt %s %s. Next: install matching binaries and restart agtd", id.Product, id.Version, version.Name, version.Product)
	}
	if missing := version.MissingAdvertised(id.Contract, id.Capabilities); len(missing) > 0 {
		return daemonIdentity{}, version.IncompatibleDaemon(base, id.Version, missing)
	}
	return id, nil
}

func matchModel(base, want string) error {
	want = strings.TrimSpace(want)
	if want == "" {
		return nil
	}
	id, err := fetchIdentity(base)
	if err != nil {
		return err
	}
	if id.Model == want {
		return nil
	}
	if id.Model == "" {
		return fmt.Errorf("agtd has no AGT_MODEL_NAME and --model is %s. Next: set AGT_MODEL_NAME=%s and restart agtd. This command does not call a hosted model", want, want)
	}
	return fmt.Errorf("agtd model is %s and --model is %s. Next: set AGT_MODEL_NAME=%s and restart agtd, or omit --model. This command does not call a hosted model", id.Model, want, want)
}

func recordedDaemonURL() string {
	path := filepath.Join(config.Load().DataDir, "agtd.addr")
	body, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	raw := strings.TrimSpace(string(body))
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return ""
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "localhost") {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return ""
		}
	}
	return strings.TrimRight(raw, "/")
}

func ensureServer(explicit string) (string, error) {
	base, pinned := clientBase(explicit)
	if err := discover(base); err == nil {
		return base, nil
	} else if pinned || !errors.Is(err, errDaemonDown) {
		return "", err
	}
	if alt := recordedDaemonURL(); alt != "" && alt != base {
		if discover(alt) == nil {
			return alt, nil
		}
	}
	release, err := acquireStartLock(config.Load().DataDir)
	if err != nil {
		return "", err
	}
	defer release()
	if err := discover(base); err == nil {
		return base, nil
	} else if !errors.Is(err, errDaemonDown) {
		return "", err
	}
	bin, err := findDaemonBinary()
	if err != nil {
		return "", fmt.Errorf("%s. Next: build agtd with `make build` and leave it beside agt, or set AGTD_BIN", err.Error())
	}
	cmd, logPath, err := startDaemon(bin)
	if err != nil {
		return "", err
	}
	deadline := time.Now().Add(8 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		last = discover(base)
		if last == nil {
			fmt.Fprintf(os.Stderr, "started agtd pid %d (log %s). It keeps running after agt exits.\n", cmd.Process.Pid, logPath)
			return base, nil
		}
		if !errors.Is(last, errDaemonDown) {
			return "", last
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("agtd pid %d did not become healthy (log %s). Next: read that log and run `agt doctor`", cmd.Process.Pid, logPath)
}

func attachCmd(args []string) int {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "agtd base URL")
	asJSON := fs.Bool("json", false, "print events as JSON")
	if err := fs.Parse(reorderFlags(args, map[string]bool{"server": true})); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: agt run attach RUN")
		return 2
	}
	base, err := ensureServer(*server)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return attach(base, rest[0], *asJSON)
}

func attach(base, runID string, jsonOut bool) int {
	var last int64
	var detached bool
	for attempt := 1; attempt <= 5; attempt++ {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		err := readEventStream(ctx, base, runID, &last, jsonOut)
		interrupted := ctx.Err() != nil
		stop()
		if interrupted {
			detached = true
			fmt.Fprintf(os.Stderr, "detached from %s. The run was not cancelled. Next: agt run attach %s, or agt run cancel %s\n", runID, runID, runID)
			break
		}
		view, getErr := fetchRun(base, runID)
		if getErr != nil {
			if attempt == 5 || (err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, errStreamIdle)) {
				fmt.Fprintln(os.Stderr, getErr)
				return 1
			}
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
			continue
		}
		if !stillRunning(view.State) {
			return renderRun(base, runID, view, jsonOut)
		}
		if attempt == 5 {
			fmt.Fprintf(os.Stderr, "lost the event stream for %s while it was %s. Next: agt run attach %s\n", runID, view.State, runID)
			return renderRun(base, runID, view, jsonOut)
		}
		time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
	}
	if detached {
		view, err := fetchRun(base, runID)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return renderRun(base, runID, view, jsonOut)
	}
	return 1
}

func stillRunning(state string) bool {
	switch state {
	case "SUCCEEDED", "CANCELLED", "FAILED_TERMINAL", "PAUSED", "PAUSED_BUDGET", "AWAITING_APPROVAL":
		return false
	default:
		return true
	}
}

type runView struct {
	State               string          `json:"state"`
	SpentMicros         int64           `json:"spent_micros"`
	BudgetCents         int             `json:"budget_cents"`
	Worktree            string          `json:"worktree"`
	RepoPath            string          `json:"repo_path"`
	Error               string          `json:"error"`
	TaskType            string          `json:"task_type"`
	ContractDigest      string          `json:"contract_digest"`
	VerificationOutcome string          `json:"verification_outcome"`
	VerificationDigest  string          `json:"verification_digest"`
	Contract            json.RawMessage `json:"contract"`
	Baseline            json.RawMessage `json:"baseline"`
	Final               json.RawMessage `json:"final"`
	Result              json.RawMessage `json:"result"`
}

func verificationLines(view runView) []string {
	var lines []string
	if view.TaskType != "" {
		lines = append(lines, "task "+view.TaskType)
	}
	if view.VerificationOutcome != "" {
		lines = append(lines, "verification "+view.VerificationOutcome)
	}
	if view.ContractDigest != "" {
		lines = append(lines, "contract "+view.ContractDigest)
	}
	lines = append(lines, contractLines(view.Contract)...)
	failures := failureLines(view.Final)
	if len(failures) == 0 {
		failures = failureLines(view.Baseline)
	}
	for _, line := range failures {
		lines = append(lines, "failure "+line)
	}
	return lines
}

func contractLines(raw json.RawMessage) []string {
	contract, err := verify.Load(raw)
	if err != nil {
		return nil
	}
	lines := make([]string, 0, len(contract.Checks))
	for _, check := range contract.Checks {
		lines = append(lines, "check "+check.Role+" "+check.Name+" "+strings.Join(check.Argv, " "))
	}
	return lines
}

func failureLines(raw json.RawMessage) []string {
	var doc struct {
		Checks []verify.CheckResult `json:"checks"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return verify.FailureLines(doc.Checks)
}

func fetchRun(base, runID string) (runView, error) {
	var view runView
	if err := getJSON(base+"/v1/runs/"+runID, &view); err != nil {
		return runView{}, err
	}
	return view, nil
}

func readEventStream(ctx context.Context, base, runID string, last *int64, jsonOut bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/runs/%s/events?stream=1&after=%d", base, runID, *last), nil)
	if err != nil {
		return err
	}
	if err := setAuth(req); err != nil {
		return err
	}
	resp, err := streamHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("events: %s", strings.TrimSpace(string(body)))
	}
	body := &idleReader{rc: resp.Body, idle: streamIdle}
	err = readSSE(body, func(seq int64, event string, data []byte) {
		if seq > 0 && seq <= *last {
			return
		}
		if seq > *last {
			*last = seq
		}
		emitEvent(seq, event, data, jsonOut)
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

var streamHTTP = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 10 * time.Second}}

type idleReader struct {
	rc   io.ReadCloser
	idle time.Duration
}

func (b *idleReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := b.rc.Read(p)
		ch <- result{n, err}
	}()
	timer := time.NewTimer(b.idle)
	defer timer.Stop()
	select {
	case res := <-ch:
		return res.n, res.err
	case <-timer.C:
		_ = b.rc.Close()
		return 0, errStreamIdle
	}
}

func emitEvent(seq int64, event string, data []byte, jsonOut bool) {
	if jsonOut {
		payload := json.RawMessage(data)
		if !json.Valid(payload) {
			raw, _ := json.Marshal(string(data))
			payload = raw
		}
		line, err := json.Marshal(struct {
			Seq   int64           `json:"seq"`
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}{Seq: seq, Event: event, Data: payload})
		if err == nil {
			fmt.Println(string(line))
		}
		return
	}
	writeLine(formatEvent(event, data))
}

func readSSE(r io.Reader, fn func(seq int64, event string, data []byte)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 2<<20)
	var event string
	var seq int64
	var data bytes.Buffer
	flush := func() {
		if event == "" && data.Len() == 0 {
			return
		}
		name := event
		if name == "" {
			name = "message"
		}
		fn(seq, name, append([]byte(nil), data.Bytes()...))
		event = ""
		seq = 0
		data.Reset()
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "id:") {
			n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64)
			if err == nil {
				seq = n
			}
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

func renderRun(base, runID string, view runView, jsonOut bool) int {
	next := nextCommands(runID, view.State, "")
	if view.State == "SUCCEEDED" {
		var diff struct {
			Digest string `json:"digest"`
		}
		if err := getJSON(base+"/v1/runs/"+runID+"/diff", &diff); err == nil && diff.Digest != "" {
			next = nextCommands(runID, view.State, diff.Digest)
		}
	}
	if jsonOut {
		summary := map[string]any{
			"id": runID, "state": view.State, "spent_micros": view.SpentMicros,
			"budget_cents": view.BudgetCents, "repo_path": view.RepoPath, "worktree": view.Worktree,
			"task_type": view.TaskType, "verification_outcome": view.VerificationOutcome,
			"contract_digest": view.ContractDigest, "verification_digest": view.VerificationDigest,
			"next": next,
		}
		if len(view.Result) > 0 && string(view.Result) != "null" {
			summary["result"] = view.Result
		}
		if view.Error != "" {
			summary["error"] = view.Error
		}
		raw, _ := json.Marshal(summary)
		fmt.Println(string(raw))
	} else {
		writeHumanRun(runID, view, next)
	}
	return exitForState(view)
}

func writeHumanRun(runID string, view runView, next []string) {
	if len(view.Result) > 0 && string(view.Result) != "null" {
		var result struct {
			Summary string `json:"summary"`
			Diff    string `json:"diff"`
		}
		_ = json.Unmarshal(view.Result, &result)
		if result.Summary != "" {
			fmt.Printf("\n%s\n", result.Summary)
		}
		fmt.Printf("cost %s budget %d cents\n", usage.USD(view.SpentMicros), view.BudgetCents)
		if result.Diff != "" {
			fmt.Printf("\n%s", result.Diff)
			if !strings.HasSuffix(result.Diff, "\n") {
				fmt.Println()
			}
		}
	} else {
		fmt.Printf("state %s\ncost %s budget %d cents\n", view.State, usage.USD(view.SpentMicros), view.BudgetCents)
	}
	for _, line := range verificationLines(view) {
		fmt.Println(line)
	}
	if view.RepoPath != "" {
		fmt.Printf("repo %s\n", view.RepoPath)
	}
	if view.Worktree != "" {
		fmt.Printf("worktree %s\n", view.Worktree)
	}
	for _, line := range next {
		fmt.Printf("next: %s\n", line)
	}
	if view.State == "AWAITING_APPROVAL" {
		fmt.Fprintln(os.Stderr, "approval required: agt approve "+runID+" COMMAND --args-hash HASH")
	}
	if view.Error != "" && view.State != "SUCCEEDED" && view.State != "AWAITING_APPROVAL" && view.State != "PAUSED_BUDGET" {
		fmt.Fprintln(os.Stderr, view.Error)
	}
}

func nextCommands(runID, state, digest string) []string {
	switch state {
	case "SUCCEEDED":
		lines := []string{
			"agt run diff " + runID,
			"agt run export " + runID + " --output agt-patch.json",
		}
		if digest != "" {
			lines = append(lines, "agt run apply "+runID+" --digest "+digest)
		} else {
			lines = append(lines, "agt run apply "+runID)
		}
		return lines
	case "AWAITING_APPROVAL":
		return []string{"agt approve " + runID + " COMMAND --args-hash HASH"}
	case "PAUSED":
		return []string{"agt run resume " + runID, "agt run attach " + runID}
	case "PAUSED_BUDGET":
		return []string{"agt run budget-resume " + runID + " --budget-cents N", "agt run attach " + runID}
	case "CANCELLED", "FAILED_TERMINAL":
		return []string{"agt run status " + runID}
	default:
		return []string{"agt run attach " + runID, "agt run status " + runID}
	}
}

func exitForState(view runView) int {
	switch view.State {
	case "SUCCEEDED":
		return 0
	case "AWAITING_APPROVAL":
		return 3
	case "PAUSED_BUDGET":
		return 4
	default:
		return 1
	}
}
