package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Session is one conversation bound to a repository.
type Session struct {
	ID       string `json:"id"`
	RepoPath string `json:"repo_path"`
	Title    string `json:"title"`
}

// SessionMessage is one ordered conversation row.
type SessionMessage struct {
	Seq             int64  `json:"seq"`
	ClientMessageID string `json:"client_message_id"`
	Role            string `json:"role"`
	Kind            string `json:"kind"`
	Content         string `json:"content"`
	RunID           string `json:"run_id"`
}

// PendingPatch is an unapplied patch from an earlier run.
type PendingPatch struct {
	RunID   string `json:"run_id"`
	Digest  string `json:"digest"`
	Outcome string `json:"outcome"`
	Change  string `json:"change"`
	Summary string `json:"summary"`
	Note    string `json:"note"`
}

// SessionView is a session plus its history. Opening it does not start a run.
type SessionView struct {
	Session     Session          `json:"session"`
	Messages    []SessionMessage `json:"messages"`
	Runs        []Run            `json:"runs"`
	ActiveRunID string           `json:"active_run_id"`
	Pending     []PendingPatch   `json:"pending"`
}

// Submission is the result of one client message id.
type Submission struct {
	SessionID       string         `json:"session_id"`
	RunID           string         `json:"run_id"`
	State           string         `json:"state"`
	Created         bool           `json:"created"`
	ClientMessageID string         `json:"client_message_id"`
	ActiveRunID     string         `json:"active_run_id"`
	Notice          string         `json:"notice"`
	Pending         []PendingPatch `json:"pending"`
	Error           string         `json:"error"`
}

// BusyError means the session already has an active run. Its goal was not changed.
type BusyError struct {
	RunID   string
	Message string
}

func (e *BusyError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "a run is already active (" + e.RunID + ")"
}

// RepoMismatchError means the session was not moved to another repository.
type RepoMismatchError struct {
	RepoPath string
	Message  string
}

func (e *RepoMismatchError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "session is bound to " + e.RepoPath
}

// CreateSession stores an empty conversation. It does not start a run.
func (c *Client) CreateSession(ctx context.Context, repo, title string) (Session, error) {
	var body struct {
		Session Session `json:"session"`
		Error   string  `json:"error"`
	}
	if err := c.post(ctx, "/v1/sessions", map[string]any{"repo_path": repo, "title": title}, &body); err != nil {
		return Session{}, err
	}
	if body.Error != "" {
		return Session{}, fmt.Errorf("%s", body.Error)
	}
	return body.Session, nil
}

// ListSessions returns conversations for one repository.
func (c *Client) ListSessions(ctx context.Context, repo string) ([]Session, error) {
	var body struct {
		Sessions []Session `json:"sessions"`
	}
	q := url.Values{}
	q.Set("repo_path", repo)
	if err := c.get(ctx, "/v1/sessions?"+q.Encode(), &body, true); err != nil {
		return nil, err
	}
	return body.Sessions, nil
}

// OpenSession checks the repository and returns history. It does not start a run.
func (c *Client) OpenSession(ctx context.Context, sessionID, repo string) (SessionView, error) {
	return c.sessionPost(ctx, "/v1/sessions/"+url.PathEscape(sessionID)+"/open", map[string]any{"repo_path": repo})
}

// ReadSession returns history. A repo that does not match the binding is refused.
func (c *Client) ReadSession(ctx context.Context, sessionID, repo string) (SessionView, error) {
	path := "/v1/sessions/" + url.PathEscape(sessionID)
	if repo != "" {
		q := url.Values{}
		q.Set("repo_path", repo)
		path += "?" + q.Encode()
	}
	var view SessionView
	err := c.get(ctx, path, &view, true)
	return view, err
}

// SubmitMessage stores one prompt. The same client message id cannot create a second run.
func (c *Client) SubmitMessage(ctx context.Context, sessionID string, req CreateRequest, clientMessageID, content string) (Submission, error) {
	body := map[string]any{
		"goal":              req.Goal,
		"content":           content,
		"repo_path":         req.RepoPath,
		"provider":          req.Provider,
		"budget_cents":      req.BudgetCents,
		"interaction":       req.Interaction,
		"profile":           req.Profile,
		"client_message_id": clientMessageID,
	}
	if req.ScriptPath != "" {
		body["script_path"] = req.ScriptPath
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Submission{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/v1/sessions/"+url.PathEscape(sessionID)+"/messages", bytes.NewReader(raw))
	if err != nil {
		return Submission{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if err := c.auth(httpReq); err != nil {
		return Submission{}, err
	}
	resp, err := c.http().Do(httpReq)
	if err != nil {
		return Submission{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Submission{}, err
	}
	var sub Submission
	_ = json.Unmarshal(data, &sub)
	if resp.StatusCode == http.StatusConflict {
		if sub.ActiveRunID != "" {
			return sub, &BusyError{RunID: sub.ActiveRunID, Message: sub.Error}
		}
		return sub, &RepoMismatchError{Message: sub.Error}
	}
	if resp.StatusCode >= 300 {
		if sub.Error != "" {
			return sub, fmt.Errorf("%s", sub.Error)
		}
		return sub, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return sub, nil
}

func (c *Client) sessionPost(ctx context.Context, path string, body any) (SessionView, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return SessionView{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, bytes.NewReader(raw))
	if err != nil {
		return SessionView{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if err := c.auth(req); err != nil {
		return SessionView{}, err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return SessionView{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return SessionView{}, err
	}
	if resp.StatusCode == http.StatusConflict {
		var wrapped struct {
			Error    string `json:"error"`
			RepoPath string `json:"repo_path"`
		}
		_ = json.Unmarshal(data, &wrapped)
		return SessionView{}, &RepoMismatchError{RepoPath: wrapped.RepoPath, Message: wrapped.Error}
	}
	if resp.StatusCode >= 300 {
		return SessionView{}, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var view SessionView
	if err := json.Unmarshal(data, &view); err != nil {
		return SessionView{}, err
	}
	return view, nil
}
