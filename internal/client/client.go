// Package client is the daemon API used by the terminal session.
// Authority stays in agtd. This package does not apply a patch on its own
// and does not accept a sandbox image.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/csy20/agt/internal/auth"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/version"
)

// Client talks to one loopback agtd. Token is the local bearer token.
type Client struct {
	Base  string
	Token string
	HTTP  *http.Client
}

// Open reads the local token and checks that base is this product and version.
func Open(base string) (*Client, error) {
	token, err := auth.Read(config.CurrentPlaces().Data)
	if err != nil {
		return nil, err
	}
	c := &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: http.DefaultClient}
	if _, err := c.Identity(context.Background()); err != nil {
		return nil, err
	}
	return c, nil
}

// New builds a client for tests and for a base the caller already discovered.
func New(base, token string) *Client {
	return &Client{Base: strings.TrimRight(base, "/"), Token: token, HTTP: http.DefaultClient}
}

// Identity is the unauthenticated daemon record. It has no API key and no bearer token.
type Identity struct {
	Product      string   `json:"product"`
	Version      string   `json:"version"`
	Contract     string   `json:"contract"`
	Build        string   `json:"build"`
	Capabilities []string `json:"capabilities"`
	Provider     string   `json:"provider"`
	Model        string   `json:"model"`
	ModelConfig  string   `json:"model_config"`
	Scripted     bool     `json:"scripted"`
	Listen       string   `json:"listen"`
}

func (c *Client) Identity(ctx context.Context) (Identity, error) {
	var id Identity
	if err := c.get(ctx, "/v1/identity", &id, false); err != nil {
		return Identity{}, err
	}
	if id.Product != version.Name || id.Version != version.Product {
		return Identity{}, fmt.Errorf("identity %s %s does not match this agt %s %s", id.Product, id.Version, version.Name, version.Product)
	}
	if missing := version.MissingAdvertised(id.Contract, id.Capabilities); len(missing) > 0 {
		return Identity{}, version.IncompatibleDaemon(c.Base, id.Version, missing)
	}
	return id, nil
}

// CreateRequest is a run the daemon will snapshot. Interaction is agent, ask, or plan.
// Profile carries a name and argv. The image is not a client field.
type CreateRequest struct {
	Goal        string
	RepoPath    string
	Provider    string
	ScriptPath  string
	BudgetCents int
	Interaction string
	// TaskType is repair, feature, or refactor. Empty stays repair.
	TaskType string
	// Contract is the verification object. The daemon does not receive a path.
	Contract    json.RawMessage
	Profile     profile.Request
	ModelConfig string
}

// Created is the create response.
type Created struct {
	RunID string `json:"run_id"`
	State string `json:"state"`
	Error string `json:"error"`
}

func (c *Client) CreateRun(ctx context.Context, req CreateRequest) (Created, error) {
	body := map[string]any{
		"goal":         req.Goal,
		"repo_path":    req.RepoPath,
		"provider":     req.Provider,
		"budget_cents": req.BudgetCents,
		"interaction":  req.Interaction,
		"profile":      req.Profile,
	}
	if req.TaskType != "" {
		body["task_type"] = req.TaskType
	}
	if len(bytes.TrimSpace(req.Contract)) > 0 {
		body["contract"] = req.Contract
	}
	if req.ScriptPath != "" {
		body["script_path"] = req.ScriptPath
	}
	if req.ModelConfig != "" {
		body["model_config"] = req.ModelConfig
	}
	if req.Provider == "" && req.ModelConfig != "" {
		delete(body, "provider")
	}
	var created Created
	if err := c.post(ctx, "/v1/runs", body, &created); err != nil {
		return Created{}, err
	}
	if created.Error != "" {
		return Created{}, fmt.Errorf("%s", created.Error)
	}
	return created, nil
}

// Run is the durable run view.
type Run struct {
	ID                  string          `json:"id"`
	State               string          `json:"state"`
	RepoPath            string          `json:"repo_path"`
	Worktree            string          `json:"worktree"`
	SpentMicros         int64           `json:"spent_micros"`
	BudgetCents         int             `json:"budget_cents"`
	Error               string          `json:"error"`
	Interaction         string          `json:"interaction"`
	TaskType            string          `json:"task_type"`
	ContractDigest      string          `json:"contract_digest"`
	VerificationOutcome string          `json:"verification_outcome"`
	VerificationDigest  string          `json:"verification_digest"`
	Provider            string          `json:"provider"`
	Result              json.RawMessage `json:"result"`
	Contract            json.RawMessage `json:"contract"`
	Baseline            json.RawMessage `json:"baseline"`
	Final               json.RawMessage `json:"final"`
	Profile             json.RawMessage `json:"profile"`
	UsageMicros         int64           `json:"usage_micros"`
}

func (c *Client) Run(ctx context.Context, runID string) (Run, error) {
	var view Run
	err := c.get(ctx, "/v1/runs/"+runID, &view, true)
	return view, err
}

// Command is one policy decision. ArgsHash binds an approval.
type Command struct {
	CommandID string          `json:"command_id"`
	Tool      string          `json:"tool"`
	Canonical json.RawMessage `json:"canonical_args"`
	ArgsHash  string          `json:"args_hash"`
	State     string          `json:"state"`
}

func (c *Client) Command(ctx context.Context, runID, commandID string) (Command, error) {
	var cmd Command
	err := c.get(ctx, "/v1/runs/"+runID+"/commands/"+commandID, &cmd, true)
	return cmd, err
}

// Diff is the preview plus the artifact digest. The preview can be shorter than the artifact.
type Diff struct {
	Diff     string `json:"diff"`
	Digest   string `json:"digest"`
	Complete bool   `json:"complete"`
	Note     string `json:"note"`
	RepoPath string `json:"repo_path"`
}

func (c *Client) Diff(ctx context.Context, runID string) (Diff, error) {
	var view Diff
	err := c.get(ctx, "/v1/runs/"+runID+"/diff", &view, true)
	return view, err
}

// Approve posts a decision bound to the command's argument hash.
// decision is approved or rejected. A mismatched hash is refused by the daemon.
func (c *Client) Approve(ctx context.Context, runID, commandID, argsHash, decision string) (Run, error) {
	var view Run
	err := c.post(ctx, "/v1/runs/"+runID+"/approvals", map[string]any{
		"command_id": commandID,
		"args_hash":  argsHash,
		"decision":   decision,
		"approver":   "local",
	}, &view)
	return view, err
}

// Apply writes the recorded repository only when digest matches the daemon artifact.
func (c *Client) Apply(ctx context.Context, runID, digest string) error {
	var view struct {
		Applied int    `json:"applied"`
		Error   string `json:"error"`
	}
	if err := c.post(ctx, "/v1/runs/"+runID+"/apply", map[string]any{"digest": digest}, &view); err != nil {
		return err
	}
	if view.Error != "" {
		return fmt.Errorf("%s", view.Error)
	}
	return nil
}

// Lifecycle posts pause, resume, cancel, or budget-resume.
func (c *Client) Lifecycle(ctx context.Context, runID, action string, body any) (Run, error) {
	if body == nil {
		body = map[string]any{}
	}
	var view Run
	err := c.post(ctx, "/v1/runs/"+runID+"/"+action, body, &view)
	return view, err
}

// Event is one server-sent run event. Data is the JSON payload.
type Event struct {
	Seq  int64
	Type string
	Data json.RawMessage
}

// ReadEvents follows the event stream after the given sequence.
// Heartbeat comments are ignored. The callback runs for each event.
func (c *Client) ReadEvents(ctx context.Context, runID string, after int64, fn func(Event) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/runs/%s/events?stream=1&after=%d", c.Base, runID, after), nil)
	if err != nil {
		return err
	}
	if err := c.auth(req); err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("events: %s", strings.TrimSpace(string(body)))
	}
	return readSSE(resp.Body, fn)
}

func (c *Client) post(ctx context.Context, path string, body any, dest any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem_"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := c.auth(req); err != nil {
		return err
	}
	return c.finish(req, dest)
}

func (c *Client) get(ctx context.Context, path string, dest any, authed bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	if authed {
		if err := c.auth(req); err != nil {
			return err
		}
	}
	return c.finish(req, dest)
}

func (c *Client) finish(req *http.Request, dest any) error {
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
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
	if dest == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, dest)
}

func (c *Client) auth(req *http.Request) error {
	if c.Token == "" {
		return fmt.Errorf("local API token is empty")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	return nil
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func readSSE(r io.Reader, fn func(Event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 2<<20)
	var event string
	var seq int64
	var data bytes.Buffer
	flush := func() error {
		if event == "" && data.Len() == 0 {
			return nil
		}
		name := event
		if name == "" {
			name = "message"
		}
		err := fn(Event{Seq: seq, Type: name, Data: append([]byte(nil), data.Bytes()...)})
		event = ""
		seq = 0
		data.Reset()
		return err
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
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
	if err := flush(); err != nil {
		return err
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return io.EOF
}
