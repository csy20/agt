package agent

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	codexCanaryText    = "agt-boundary-canary"
	codexNetCanary     = "agt-net-canary"
	codexProfile       = "agt-snapshot"
	codexAllowedName   = "agt-allowed.txt"
	maxCodexLine       = 1 << 20
	maxCodexPrompt     = 8000
	maxCodexTurnPrompt = 16000
	// promptOnlyLimitation is the Ask contract when agent tools are disabled.
	promptOnlyLimitation = "prompt-only; agent tools disabled; tagged excerpts only"
)

// AskRequest is one read-only Codex app-server turn.
// Authorize is an explicit grant for this call. A saved login is not enough.
// Command is the Codex executable and any leading arguments. Empty means "codex".
type AskRequest struct {
	Command   []string
	WorkDir   string
	Prompt    string
	Excerpts  []string
	Authorize bool
	Mutating  bool
}

// AskResult is the evidence from one ask attempt.
// Label is "blocked" when no turn was started, "scripted" for a stub or an
// unvalidated runtime, and "live" only when the launched executable's own
// version matches the app-server user agent and the turn completes.
// A basename of "codex" is not that check. CostKnown stays false: a ChatGPT
// subscription session does not report an AGT bill.
type AskResult struct {
	Label          string   `json:"label"`
	Live           bool     `json:"live"`
	TurnStarted    bool     `json:"turn_started"`
	PromptSent     bool     `json:"prompt_sent"`
	Outcome        string   `json:"outcome,omitempty"`
	Text           string   `json:"text,omitempty"`
	Backend        string   `json:"backend,omitempty"`
	RuntimeVersion string   `json:"runtime_version,omitempty"`
	RuntimeDigest  string   `json:"runtime_digest,omitempty"`
	Limitation     string   `json:"limitation,omitempty"`
	Model          string   `json:"model,omitempty"`
	ThreadID       string   `json:"thread_id,omitempty"`
	TurnID         string   `json:"turn_id,omitempty"`
	SentMethods    []string `json:"sent_methods,omitempty"`
	Denied         []string `json:"denied,omitempty"`
	FilesUnchanged bool     `json:"files_unchanged"`
	SnapshotHash   string   `json:"snapshot_hash,omitempty"`
	UsageKnown     bool     `json:"usage_known"`
	InputTokens    int      `json:"input_tokens,omitempty"`
	OutputTokens   int      `json:"output_tokens,omitempty"`
	CostKnown      bool     `json:"cost_known"`
}

// BoundaryError means app-server was not allowed to start a turn.
// TurnStarted is false when the prompt was not sent.
type BoundaryError struct {
	Backend      string
	OutsideRead  bool
	WriteAllowed bool
	TurnStarted  bool
	Detail       string
}

func (e *BoundaryError) Error() string {
	return e.Detail
}

func (e *BoundaryError) Unwrap() error { return ErrBoundary }

// LoginError means the Codex CLI is not in a ChatGPT subscription session.
// API-key billing is not a substitute.
type LoginError struct {
	Status string
}

func (e *LoginError) Error() string {
	status := strings.TrimSpace(e.Status)
	if status == "" {
		status = "no status"
	}
	return "Codex subscription login is required. Run `codex login` and choose ChatGPT. Do not set CODEX_API_KEY or OPENAI_API_KEY for this path. codex login status: " + status
}

func (e *LoginError) Unwrap() error { return ErrLogin }

// Ask launches Codex app-server, checks the agt-snapshot permission profile,
// and only then starts one prompt-only turn. A failing preflight closes the
// process before turn/start. Agent tools stay disabled. Writes, command
// approvals, and unknown tool requests are declined. The snapshot directory
// is not an AGT Docker sandbox; the profile preflight is the gate.
func Ask(ctx context.Context, req AskRequest) (res AskResult, err error) {
	res.Label = "blocked"
	res.FilesUnchanged = true
	if req.Mutating {
		return res, fmt.Errorf("%w: mutating Codex execution is not enabled", ErrMutationUnavailable)
	}
	if !req.Authorize {
		return res, fmt.Errorf("%w: this call has no authorization to start Codex", ErrLiveUnavailable)
	}
	work, err := cleanWorkDir(req.WorkDir)
	if err != nil {
		return res, err
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" || len(prompt) > maxCodexPrompt {
		return res, fmt.Errorf("prompt must be 1 to %d characters", maxCodexPrompt)
	}
	before, err := snapshotHash(work)
	if err != nil {
		return res, err
	}
	res.SnapshotHash = before
	defer func() {
		after, herr := snapshotHash(work)
		if herr != nil || after != before {
			res.FilesUnchanged = false
		}
	}()

	base := req.Command
	if len(base) == 0 {
		base = []string{"codex"}
	}
	status, err := loginStatus(ctx, base)
	if err != nil {
		return res, err
	}
	if !chatgptLogin(status) {
		return res, &LoginError{Status: clipStatus(status)}
	}
	version, digest := runtimeIdentity(ctx, base)
	res.RuntimeVersion = version
	res.RuntimeDigest = digest

	extra, err := profileArgs(work, codexRuntimeRoot(resolveCodex(base[0])))
	if err != nil {
		return res, err
	}
	session, err := startAppServer(ctx, base, extra)
	if err != nil {
		return res, err
	}
	defer session.close()

	initRaw, err := session.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "agt", "title": "AGT", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	})
	if err != nil {
		return res, err
	}
	var init struct {
		UserAgent string `json:"userAgent"`
	}
	_ = json.Unmarshal(initRaw, &init)
	res.Backend = strings.TrimSpace(init.UserAgent)
	if err := session.notify("initialized", map[string]any{}); err != nil {
		return res, err
	}
	cfgRaw, err := session.call(ctx, "config/read", map[string]any{"cwd": work, "includeLayers": false})
	res.SentMethods = session.methods()
	if err != nil {
		return res, fmt.Errorf("%w: config/read did not run: %v", ErrBoundary, err)
	}
	if err := toolsDisabled(cfgRaw); err != nil {
		return res, &BoundaryError{Backend: res.Backend, Detail: err.Error()}
	}
	outside, wrote, network, err := session.preflight(ctx, work)
	res.SentMethods = session.methods()
	if err != nil {
		return res, err
	}
	if outside || wrote || network {
		detail := "codex permission profile " + codexProfile + " cannot enforce the AGT boundary; turn/start was not sent"
		switch {
		case outside && wrote:
			detail = "codex permission profile " + codexProfile + " read a file outside the snapshot and allowed a write; turn/start was not sent"
		case outside:
			detail = "codex permission profile " + codexProfile + " read a file outside the snapshot; turn/start was not sent"
		case wrote:
			detail = "codex permission profile " + codexProfile + " allowed a write inside the snapshot; turn/start was not sent"
		case network:
			detail = "codex permission profile " + codexProfile + " allowed tool network access; turn/start was not sent"
		}
		return res, &BoundaryError{Backend: res.Backend, OutsideRead: outside, WriteAllowed: wrote, Detail: detail}
	}
	if breach := session.breachError(); breach != nil {
		return res, breach
	}

	threadRaw, err := session.call(ctx, "thread/start", map[string]any{
		"cwd":                   work,
		"approvalPolicy":        "untrusted",
		"approvalsReviewer":     "user",
		"permissions":           codexProfile,
		"runtimeWorkspaceRoots": []string{work},
		"ephemeral":             true,
		"serviceName":           "agt",
		"dynamicTools":          []any{},
		"developerInstructions": "Answer from the tagged excerpts only. Do not run commands, edit files, search the web, or call tools. Keep the answer under 200 words.",
	})
	if err != nil {
		res.SentMethods = session.methods()
		return res, err
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
		ActivePermissionProfile struct {
			ID string `json:"id"`
		} `json:"activePermissionProfile"`
		RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots"`
		Model                 string   `json:"model"`
		Sandbox               struct {
			Type          string   `json:"type"`
			NetworkAccess bool     `json:"networkAccess"`
			WritableRoots []string `json:"writableRoots"`
		} `json:"sandbox"`
	}
	if err := json.Unmarshal(threadRaw, &thread); err != nil || thread.Thread.ID == "" {
		res.SentMethods = session.methods()
		return res, fmt.Errorf("%w: thread/start returned no thread id", ErrProtocolMismatch)
	}
	if thread.ActivePermissionProfile.ID != codexProfile || !rootsContain(thread.RuntimeWorkspaceRoots, work) || thread.Sandbox.Type == "dangerFullAccess" || thread.Sandbox.Type == "workspaceWrite" || len(thread.Sandbox.WritableRoots) > 0 || thread.Sandbox.NetworkAccess {
		res.SentMethods = session.methods()
		return res, &BoundaryError{Backend: res.Backend, Detail: "codex thread/start did not keep permission profile " + codexProfile + "; turn/start was not sent"}
	}
	res.ThreadID = thread.Thread.ID
	res.Model = strings.TrimSpace(thread.Model)
	session.setThread(thread.Thread.ID)

	fullPrompt, err := taggedPrompt(work, prompt, req.Excerpts)
	if err != nil {
		res.SentMethods = session.methods()
		return res, err
	}
	res.Limitation = promptOnlyLimitation
	res.PromptSent = true
	session.arm()
	turnRaw, err := session.call(ctx, "turn/start", map[string]any{
		"threadId":          thread.Thread.ID,
		"cwd":               work,
		"approvalPolicy":    "untrusted",
		"approvalsReviewer": "user",
		"permissions":       codexProfile,
		"input":             []any{map[string]any{"type": "text", "text": fullPrompt}},
	})
	res.TurnStarted = err == nil
	res.SentMethods = session.methods()
	if err != nil {
		return res, err
	}
	var turn struct {
		Turn struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(turnRaw, &turn); err != nil || turn.Turn.ID == "" {
		return res, fmt.Errorf("%w: turn/start returned no turn id", ErrProtocolMismatch)
	}
	res.TurnID = turn.Turn.ID
	session.setTurn(turn.Turn.ID)

	outcome, text, usageIn, usageOut, usageKnown, waitErr := session.wait(ctx)
	res.SentMethods = session.methods()
	res.Denied = session.denied()
	res.Text = text
	res.Outcome = outcome
	res.UsageKnown = usageKnown
	res.InputTokens = usageIn
	res.OutputTokens = usageOut
	if breach := session.breachError(); breach != nil {
		return res, breach
	}
	if waitErr != nil {
		return res, waitErr
	}
	if outcome != string(OutcomeCompleted) {
		return res, fmt.Errorf("%w: turn ended %s", ErrProtocolMismatch, outcome)
	}
	if version != "" && digest != "" && strings.Contains(res.Backend, version) {
		res.Label = "live"
		res.Live = true
	} else {
		res.Label = LabelScripted
	}
	return res, nil
}

func cleanWorkDir(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("codex snapshot must be an absolute path")
	}
	clean := filepath.Clean(path)
	info, err := os.Stat(clean)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("codex snapshot is not a directory")
	}
	home, err := os.UserHomeDir()
	if err == nil && filepath.Clean(home) == clean {
		return "", fmt.Errorf("refusing to use the home directory as a Codex snapshot")
	}
	if strings.Contains(clean, string(filepath.Separator)+".codex") {
		return "", fmt.Errorf("refusing to use a Codex config directory as the snapshot")
	}
	return clean, nil
}

func chatgptLogin(status string) bool {
	line := strings.ToLower(status)
	if strings.Contains(line, "api key") {
		return false
	}
	return strings.Contains(status, "Logged in using ChatGPT")
}

func clipStatus(status string) string {
	status = strings.TrimSpace(status)
	if i := strings.IndexByte(status, '\n'); i >= 0 {
		status = status[:i]
	}
	status = strings.TrimSpace(status)
	if len(status) > 180 {
		status = status[:180]
	}
	return scrubStatus(status)
}

func scrubStatus(s string) string {
	lower := strings.ToLower(s)
	for _, needle := range []string{"sk-", "bearer ", "api_key", "api-key", "token"} {
		if strings.Contains(lower, needle) {
			return "status omitted"
		}
	}
	return s
}

func loginStatus(ctx context.Context, base []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, base[0], append(append([]string{}, base[1:]...), "login", "status")...)
	cmd.Env = childEnv()
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if ctx.Err() != nil {
		return "", fmt.Errorf("%w: codex login status timed out", ErrLogin)
	}
	if err != nil && text == "" {
		return "", fmt.Errorf("%w: codex login status failed", ErrLogin)
	}
	return text, nil
}

func childEnv() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "USER", "LOGNAME", "AGT_CODEX_STUB", "AGT_STUB_LOG", "AGT_STUB_MODE"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}

func snapshotHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s %o\n", rel, info.Mode())
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("snapshot has a non-regular file %s", rel)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (m rpcMessage) idKey() string {
	if len(m.ID) == 0 || string(m.ID) == "null" {
		return ""
	}
	return string(m.ID)
}

func (m rpcMessage) isResponse() bool {
	return m.idKey() != "" && m.Method == ""
}

type appSession struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	done     chan struct{}
	once     sync.Once
	stopOnce sync.Once
	wmu      sync.Mutex
	mu       sync.Mutex
	next     int
	pending  map[string]chan rpcMessage
	kinds    map[string]string
	sent     []string
	declined []string
	text     strings.Builder
	thread   string
	turn     string
	armed    bool
	breach   error
	usage    struct {
		in, out int
		known   bool
	}
	finished chan struct{}
	finOnce  sync.Once
	outcome  string
}

func startAppServer(ctx context.Context, base, extra []string) (*appSession, error) {
	args := append(append([]string{}, base[1:]...), "app-server", "--listen", "stdio://")
	args = append(args, extra...)
	cmd := exec.Command(base[0], args...)
	cmd.Env = childEnv()
	setProcGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	session := &appSession{
		cmd:      cmd,
		stdin:    stdin,
		done:     make(chan struct{}),
		next:     1,
		pending:  map[string]chan rpcMessage{},
		kinds:    map[string]string{},
		finished: make(chan struct{}),
	}
	go func() {
		session.readLoop(stdout)
		_ = cmd.Wait()
	}()
	go func() {
		select {
		case <-ctx.Done():
			session.cancelTurn()
		case <-session.done:
		}
	}()
	return session, nil
}

func (s *appSession) close() {
	s.once.Do(func() {
		_ = s.stdin.Close()
		if s.cmd.Process != nil {
			killGroup(s.cmd.Process.Pid)
			_ = s.cmd.Process.Kill()
		}
		close(s.done)
	})
}

func (s *appSession) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]string(nil), s.sent...)
	return out
}

func (s *appSession) denied() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.declined...)
}

func (s *appSession) breachError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.breach
}

func (s *appSession) setThread(id string) {
	s.mu.Lock()
	s.thread = id
	s.mu.Unlock()
}

func (s *appSession) arm() {
	s.mu.Lock()
	s.armed = true
	s.mu.Unlock()
}

func (s *appSession) setTurn(id string) {
	s.mu.Lock()
	s.turn = id
	s.armed = true
	s.mu.Unlock()
}

func (s *appSession) write(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err = s.stdin.Write(append(raw, '\n'))
	if err == nil {
		return nil
	}
	select {
	case <-s.finished:
	case <-time.After(time.Second):
	}
	if breach := s.breachError(); breach != nil {
		return breach
	}
	return err
}

func (s *appSession) notify(method string, params any) error {
	s.mu.Lock()
	s.sent = append(s.sent, method)
	s.mu.Unlock()
	return s.write(map[string]any{"method": method, "params": params})
}

func (s *appSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	id := s.next
	s.next++
	key := fmt.Sprintf("%d", id)
	ch := make(chan rpcMessage, 1)
	s.pending[key] = ch
	s.kinds[key] = method
	s.sent = append(s.sent, method)
	s.mu.Unlock()
	if err := s.write(map[string]any{"method": method, "id": id, "params": params}); err != nil {
		return nil, err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, fmt.Errorf("%s: app-server closed", method)
	}
}

func (s *appSession) readLoop(r io.Reader) {
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := readLine(reader)
		if err != nil {
			if len(line) > 0 {
				s.fail(fmt.Errorf("%w: truncated app-server line", ErrProtocolMismatch))
			}
			s.finish(string(OutcomeIncomplete))
			s.close()
			return
		}
		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			s.fail(fmt.Errorf("%w: app-server line is not JSON", ErrProtocolMismatch))
			s.finish(string(OutcomeFailed))
			s.close()
			return
		}
		if msg.isResponse() {
			s.mu.Lock()
			ch := s.pending[msg.idKey()]
			kind := s.kinds[msg.idKey()]
			delete(s.pending, msg.idKey())
			delete(s.kinds, msg.idKey())
			s.mu.Unlock()
			if kind == "turn/start" {
				var turn struct {
					Turn struct {
						ID string `json:"id"`
					} `json:"turn"`
				}
				if json.Unmarshal(msg.Result, &turn) == nil && turn.Turn.ID != "" {
					s.setTurn(turn.Turn.ID)
				}
			}
			if ch != nil {
				ch <- msg
			}
			continue
		}
		if msg.idKey() != "" && msg.Method != "" {
			go s.decline(msg)
			continue
		}
		s.note(msg)
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxCodexLine {
			return buf, fmt.Errorf("line too long")
		}
		if err == nil {
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func (s *appSession) decline(msg rpcMessage) {
	body := declineBody(msg.Method)
	raw, _ := json.Marshal(body)
	if bytesContainAccept(raw) {
		s.fail(fmt.Errorf("%w: refusal encoder produced an accept", ErrBoundary))
		return
	}
	s.mu.Lock()
	s.declined = append(s.declined, msg.Method)
	s.mu.Unlock()
	var id any
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return
	}
	_ = s.write(map[string]any{"id": id, "result": body})
	if !knownDecline(msg.Method) {
		s.fail(fmt.Errorf("%w: unknown server request %s", ErrBoundary, msg.Method))
		s.cancelTurn()
	}
}

func knownDecline(method string) bool {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval", "item/permissions/requestApproval", "mcpServer/elicitation/request", "tool/requestUserInput":
		return true
	default:
		return false
	}
}

func declineBody(method string) any {
	switch method {
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "decline", "content": nil}
	default:
		return map[string]any{"decision": "decline"}
	}
}

func bytesContainAccept(raw []byte) bool {
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return true
	}
	decision, _ := probe["decision"].(string)
	switch decision {
	case "accept", "acceptForSession", "cancel":
		return true
	}
	if _, ok := probe["acceptWithExecpolicyAmendment"]; ok {
		return true
	}
	action, _ := probe["action"].(string)
	return action == "accept"
}

func (s *appSession) note(msg rpcMessage) {
	switch msg.Method {
	case "item/agentMessage/delta":
		var p struct {
			Delta string `json:"delta"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		s.mu.Lock()
		s.text.WriteString(p.Delta)
		s.mu.Unlock()
	case "thread/tokenUsage/updated":
		var p struct {
			TokenUsage struct {
				Total struct {
					InputTokens  int `json:"inputTokens"`
					OutputTokens int `json:"outputTokens"`
				} `json:"total"`
			} `json:"tokenUsage"`
		}
		if json.Unmarshal(msg.Params, &p) == nil {
			s.mu.Lock()
			s.usage.in = p.TokenUsage.Total.InputTokens
			s.usage.out = p.TokenUsage.Total.OutputTokens
			s.usage.known = true
			s.mu.Unlock()
		}
	case "item/completed":
		s.noteCompleted(msg.Params)
	case "turn/completed":
		var p struct {
			Turn struct {
				Status string `json:"status"`
			} `json:"turn"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		switch p.Turn.Status {
		case "completed":
			s.finish(string(OutcomeCompleted))
		case "interrupted":
			s.finish(string(OutcomeCancelled))
		case "failed":
			s.finish(string(OutcomeFailed))
		default:
			s.fail(fmt.Errorf("%w: turn status %q", ErrProtocolMismatch, p.Turn.Status))
			s.finish(string(OutcomeFailed))
		}
	case "error":
		s.fail(fmt.Errorf("%w: app-server error event", ErrProtocolMismatch))
		s.finish(string(OutcomeFailed))
	}
}

func (s *appSession) noteCompleted(params json.RawMessage) {
	var wrap struct {
		Item struct {
			Type   string `json:"type"`
			Status string `json:"status"`
			Text   string `json:"text"`
		} `json:"item"`
	}
	if err := json.Unmarshal(params, &wrap); err != nil {
		s.fail(fmt.Errorf("%w: item/completed is not JSON", ErrProtocolMismatch))
		return
	}
	item := wrap.Item
	if item.Type == "agentMessage" && item.Text != "" {
		s.mu.Lock()
		if s.text.Len() == 0 {
			s.text.WriteString(item.Text)
		}
		s.mu.Unlock()
	}
	s.mu.Lock()
	armed := s.armed
	s.mu.Unlock()
	if !armed {
		return
	}
	ran := item.Status == "completed" || item.Status == "failed"
	switch item.Type {
	case "commandExecution", "fileChange", "webSearch", "mcpToolCall", "dynamicToolCall", "collabToolCall":
		if ran {
			s.fail(fmt.Errorf("%w: %s completed with status %s", ErrBoundary, item.Type, item.Status))
			go s.cancelTurn()
		}
	case "", "agentMessage", "reasoning", "plan", "userMessage":
	default:
		if ran {
			s.fail(fmt.Errorf("%w: unknown item %s", ErrBoundary, item.Type))
			go s.cancelTurn()
		}
	}
}

func (s *appSession) fail(err error) {
	s.mu.Lock()
	if s.breach == nil {
		s.breach = err
	}
	s.mu.Unlock()
}

func (s *appSession) finish(outcome string) {
	s.finOnce.Do(func() {
		s.mu.Lock()
		s.outcome = outcome
		s.mu.Unlock()
		close(s.finished)
	})
}

func (s *appSession) wait(ctx context.Context) (outcome, text string, in, out int, known bool, err error) {
	select {
	case <-ctx.Done():
		s.cancelTurn()
		select {
		case <-s.finished:
		case <-time.After(2 * time.Second):
		}
	case <-s.finished:
	case <-s.done:
		err = fmt.Errorf("app-server closed before the turn finished")
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome == "" && err != nil {
		s.outcome = string(OutcomeCancelled)
	}
	return s.outcome, s.text.String(), s.usage.in, s.usage.out, s.usage.known, err
}

func (s *appSession) cancelTurn() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		thread, turn := s.thread, s.turn
		s.mu.Unlock()
		if thread != "" && turn != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_, err := s.call(ctx, "turn/interrupt", map[string]any{"threadId": thread, "turnId": turn})
			cancel()
			if err == nil {
				// The completion notice is still in the pipe. Killing here
				// drops it and leaves the outcome incomplete.
				_ = s.stdin.Close()
				return
			}
		}
		s.close()
	})
}

func runtimeIdentity(ctx context.Context, base []string) (version, digest string) {
	vctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(vctx, base[0], append(append([]string{}, base[1:]...), "--version")...)
	cmd.Env = childEnv()
	out, err := cmd.Output()
	if err == nil && strings.Contains(string(out), "codex-cli ") {
		version = semverPattern.FindString(string(out))
	}
	path := resolveCodex(base[0])
	f, err := os.Open(path)
	if err != nil {
		return version, ""
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return version, ""
	}
	return version, hex.EncodeToString(sum.Sum(nil))
}

func resolveCodex(bin string) string {
	path := bin
	if !filepath.IsAbs(path) {
		if found, err := exec.LookPath(path); err == nil && found != "" {
			path = found
		}
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil && resolved != "" {
		return resolved
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func codexRuntimeRoot(bin string) string {
	path := resolveCodex(bin)
	dir := filepath.Dir(path)
	for i := 0; i < 6; i++ {
		if root := vendorRootNear(dir); root != "" {
			return root
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

func vendorRootNear(dir string) string {
	patterns := []string{
		filepath.Join(dir, "node_modules", "@openai", "codex-linux-x64", "vendor", "*", "bin", "codex"),
		filepath.Join(dir, "..", "node_modules", "@openai", "codex-linux-x64", "vendor", "*", "bin", "codex"),
	}
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(pattern)
		for _, match := range matches {
			info, err := os.Stat(match)
			if err != nil || info.IsDir() {
				continue
			}
			return filepath.Clean(filepath.Dir(filepath.Dir(match)))
		}
	}
	return ""
}

func profileArgs(work, runtime string) ([]string, error) {
	if err := safeCodexPath(work); err != nil {
		return nil, err
	}
	if runtime != "" {
		if err := safeCodexPath(runtime); err != nil {
			return nil, err
		}
	}
	grants := []string{`":minimal"="read"`, fmt.Sprintf("%q=%q", work, "read")}
	if runtime != "" {
		grants = append(grants, fmt.Sprintf("%q=%q", runtime, "read"))
	}
	table := "{" + strings.Join(grants, ", ") + "}"
	return []string{
		"--disable", "shell_tool",
		"--disable", "plugins",
		"--disable", "browser_use",
		"--disable", "computer_use",
		"--disable", "web_search_request",
		"--disable", "web_search_cached",
		"--disable", "apps",
		"-c", `default_permissions="agt-snapshot"`,
		"-c", "permissions." + codexProfile + ".filesystem=" + table,
		"-c", "permissions." + codexProfile + ".network.enabled=false",
		"-c", "mcp_servers.node_repl.enabled=false",
	}, nil
}

func safeCodexPath(path string) error {
	if path == "" || strings.ContainsAny(path, "\"\n\r\\") {
		return fmt.Errorf("refusing a Codex path that cannot be quoted safely")
	}
	return nil
}

func toolsDisabled(raw json.RawMessage) error {
	var body struct {
		Config struct {
			SandboxMode        json.RawMessage            `json:"sandbox_mode"`
			DefaultPermissions string                     `json:"default_permissions"`
			Permissions        map[string]json.RawMessage `json:"permissions"`
			Features           map[string]json.RawMessage `json:"features"`
			MCPServers         map[string]struct {
				Enabled *bool `json:"enabled"`
			} `json:"mcp_servers"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || (body.Config.DefaultPermissions == "" && len(body.Config.Features) == 0) {
		return fmt.Errorf("codex config/read did not describe the permission profile; turn/start was not sent")
	}
	if !jsonNullOrEmpty(body.Config.SandboxMode) {
		return fmt.Errorf("codex sandbox_mode is set and can override the permission profile; turn/start was not sent")
	}
	if !jsonFalse(body.Config.Features["shell_tool"]) {
		return fmt.Errorf("codex shell_tool is not disabled; turn/start was not sent")
	}
	if body.Config.DefaultPermissions != codexProfile {
		return fmt.Errorf("codex default_permissions is not %s; turn/start was not sent", codexProfile)
	}
	if _, ok := body.Config.Permissions[codexProfile]; !ok {
		return fmt.Errorf("codex permissions do not include %s; turn/start was not sent", codexProfile)
	}
	if node, ok := body.Config.MCPServers["node_repl"]; ok && node.Enabled != nil && *node.Enabled {
		return fmt.Errorf("codex node_repl is enabled; turn/start was not sent")
	}
	return nil
}

func jsonFalse(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "false"
}

func jsonNullOrEmpty(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return text == "" || text == "null"
}

func taggedPrompt(work, prompt string, excerpts []string) (string, error) {
	var b strings.Builder
	b.WriteString(prompt)
	b.WriteString("\n\nAgent tools are disabled. Use only the tagged excerpts below. Do not request commands, file writes, web search, or other tools.\n")
	for _, rel := range excerpts {
		clean, err := excerptPath(work, rel)
		if err != nil {
			return "", err
		}
		path := filepath.Join(work, clean)
		info, err := os.Lstat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("excerpt %q is not a regular file", rel)
		}
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		body, err := io.ReadAll(io.LimitReader(f, 16<<10+1))
		f.Close()
		if err != nil {
			return "", err
		}
		if len(body) > 16<<10 {
			return "", fmt.Errorf("excerpt %q exceeds 16384 bytes", rel)
		}
		fmt.Fprintf(&b, "\n<file path=%q>\n%s\n</file>\n", filepath.ToSlash(clean), body)
	}
	if b.Len() > maxCodexTurnPrompt {
		return "", fmt.Errorf("tagged prompt exceeds %d characters", maxCodexTurnPrompt)
	}
	return b.String(), nil
}

func excerptPath(work, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "..") {
		return "", fmt.Errorf("excerpt %q is not a relative snapshot path", rel)
	}
	clean := filepath.Clean(rel)
	full := filepath.Join(work, clean)
	got, err := filepath.Rel(work, full)
	if err != nil || got == ".." || strings.HasPrefix(got, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("excerpt %q is not a relative snapshot path", rel)
	}
	return clean, nil
}

func rootsContain(roots []string, work string) bool {
	want := filepath.Clean(work)
	for _, root := range roots {
		if filepath.Clean(root) == want {
			return true
		}
	}
	return false
}

func (s *appSession) preflight(ctx context.Context, work string) (outside, wrote, network bool, err error) {
	allowed := filepath.Join(work, codexAllowedName)
	link := filepath.Join(work, "agt-escape")
	homeLink := filepath.Join(work, "agt-home-escape")
	writePath := filepath.Join(work, "agt-boundary-write")
	var sibling, unrelated, homeCanary string
	defer func() {
		_ = os.Remove(allowed)
		_ = os.Remove(link)
		_ = os.Remove(homeLink)
		_ = os.Remove(writePath)
		if sibling != "" {
			_ = os.RemoveAll(sibling)
		}
		if unrelated != "" {
			_ = os.RemoveAll(unrelated)
		}
		if homeCanary != "" {
			_ = os.Remove(homeCanary)
		}
	}()
	sibling, err = os.MkdirTemp(filepath.Dir(work), "agt-sibling-")
	if err != nil {
		return false, false, false, err
	}
	unrelated, err = os.MkdirTemp(filepath.Dir(work), "agt-unrelated-")
	if err != nil {
		return false, false, false, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false, false, false, err
	}
	homeFile, err := os.CreateTemp(home, ".agt-canary-")
	if err != nil {
		return false, false, false, err
	}
	homeCanary = homeFile.Name()
	payload := []byte(codexCanaryText + "\n")
	if _, err := homeFile.Write(payload); err != nil {
		homeFile.Close()
		return false, false, false, err
	}
	if err := homeFile.Close(); err != nil {
		return false, false, false, err
	}
	if err := os.Chmod(homeCanary, 0o600); err != nil {
		return false, false, false, err
	}
	siblingFile := filepath.Join(sibling, "credentials.txt")
	unrelatedFile := filepath.Join(unrelated, "secret.txt")
	for _, path := range []string{allowed, siblingFile, unrelatedFile} {
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			return false, false, false, err
		}
	}
	if err := os.Symlink(siblingFile, link); err != nil {
		return false, false, false, err
	}
	if err := os.Symlink(homeCanary, homeLink); err != nil {
		return false, false, false, err
	}
	relSibling, err := filepath.Rel(work, siblingFile)
	if err != nil {
		return false, false, false, err
	}
	code, stdout, stderr, err := s.execProfile(ctx, work, []string{"cat", codexAllowedName})
	if err != nil {
		return false, false, false, err
	}
	if code != 0 || !strings.Contains(stdout, codexCanaryText) {
		return false, false, false, fmt.Errorf("%w: permission profile %s could not read the snapshot (exit %d: %s); turn/start was not sent", ErrBoundary, codexProfile, code, clipStatus(stderr))
	}
	for _, argv := range [][]string{
		{"cat", siblingFile},
		{"cat", relSibling},
		{"cat", "agt-escape"},
		{"cat", "agt-home-escape"},
		{"cat", homeCanary},
		{"cat", unrelatedFile},
	} {
		code, stdout, stderr, execErr := s.execProfile(ctx, work, argv)
		if execErr != nil {
			return outside, false, false, execErr
		}
		if code == 0 || strings.Contains(stdout, codexCanaryText) || strings.Contains(stderr, codexCanaryText) {
			outside = true
		}
	}
	code, _, _, err = s.execProfile(ctx, work, []string{"touch", "agt-boundary-write"})
	if err != nil {
		return outside, false, false, err
	}
	if _, statErr := os.Stat(writePath); code == 0 || statErr == nil {
		wrote = true
	}
	network, err = s.networkProbe(ctx, work)
	return outside, wrote, network, err
}

func (s *appSession) networkProbe(ctx context.Context, work string) (bool, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return false, err
	}
	defer ln.Close()
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now().Add(12 * time.Second))
	}
	go func() {
		conn, acceptErr := ln.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 2048)
		_, _ = conn.Read(buf)
		body := codexNetCanary + "\n"
		_, _ = fmt.Fprintf(conn, "HTTP/1.0 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(body), body)
	}()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return false, fmt.Errorf("%w: network preflight listener has no port", ErrBoundary)
	}
	script := fmt.Sprintf("import urllib.request; print(urllib.request.urlopen('http://127.0.0.1:%d', timeout=3).read().decode())", addr.Port)
	code, stdout, stderr, err := s.execProfile(ctx, work, []string{"python3", "-c", script})
	if err != nil {
		return false, err
	}
	if strings.Contains(stdout, codexNetCanary) || strings.Contains(stderr, codexNetCanary) {
		return true, nil
	}
	if code != 0 && interpreterMissing(stderr) {
		return false, fmt.Errorf("%w: python3 was not available for the network preflight; turn/start was not sent", ErrBoundary)
	}
	return false, nil
}

func interpreterMissing(stderr string) bool {
	low := strings.ToLower(stderr)
	if strings.Contains(low, "traceback") {
		return false
	}
	return strings.Contains(low, "no such file") || strings.Contains(low, "not found")
}

func (s *appSession) execProfile(ctx context.Context, work string, argv []string) (int, string, string, error) {
	raw, err := s.call(ctx, "command/exec", map[string]any{
		"command":           argv,
		"cwd":               work,
		"timeoutMs":         8000,
		"permissionProfile": codexProfile,
	})
	if err != nil {
		return 0, "", "", fmt.Errorf("%w: command/exec did not run: %v", ErrBoundary, err)
	}
	var body struct {
		ExitCode int    `json:"exitCode"`
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return 0, "", "", fmt.Errorf("%w: command/exec response", ErrProtocolMismatch)
	}
	return body.ExitCode, body.Stdout, body.Stderr, nil
}
