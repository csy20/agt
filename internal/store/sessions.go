package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/jackc/pgx/v5"
)

// Session is one conversation. It is bound to a single repository path.
// Runs linked from the session keep their own snapshot, budget, and result.
type Session struct {
	ID          string
	RepoPath    string
	Title       string
	ModelConfig string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SessionMessage is one ordered row in a conversation.
type SessionMessage struct {
	SessionID       string
	Seq             int64
	ClientMessageID string
	Role            string
	Kind            string
	Content         string
	RunID           string
	CreatedAt       time.Time
}

// PendingPatch is an earlier run whose patch is still unapplied.
// Change is proposed or verified. Applied patches are not pending.
type PendingPatch struct {
	RunID   string
	Digest  string
	Outcome string
	Change  string
	Summary string
	Note    string
}

// SubmitInput stores one user prompt and, when the session is idle, one new run.
type SubmitInput struct {
	SessionID       string
	ClientMessageID string
	Content         string
	Create          CreateInput
	Pending         []PendingPatch
}

// SubmitResult is the durable outcome of one client message id.
type SubmitResult struct {
	Message SessionMessage
	Run     Run
	Created bool
	Pending []PendingPatch
}

const sessionColumns = `id, repo_path, title, model_config, created_at, updated_at`

func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// ValidClientMessageID reports whether id can be stored and retried.
func ValidClientMessageID(raw string) bool {
	if raw == "" || len(raw) > 200 {
		return false
	}
	for _, r := range raw {
		if r < 0x21 || r > 0x7e || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// SetSessionModel saves the session's preferred configuration name.
// It does not create a run and does not call a model.
func (s *Store) SetSessionModel(ctx context.Context, id, name string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE sessions SET model_config=$2, updated_at=now() WHERE id=$1`, id, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateSession(ctx context.Context, repo, title string) (Session, error) {
	if strings.TrimSpace(repo) == "" {
		return Session{}, fmt.Errorf("repo_path is required")
	}
	sess := Session{ID: id.New("ses_"), RepoPath: repo, Title: clipTitle(title)}
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO sessions (id, repo_path, title) VALUES ($1,$2,$3)`,
			sess.ID, sess.RepoPath, sess.Title)
		if err != nil {
			return err
		}
		var errScan error
		sess, errScan = scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1`, sess.ID))
		return errScan
	})
	return sess, err
}

func (s *Store) GetSession(ctx context.Context, sessionID string) (Session, error) {
	return scanSession(s.pool.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1`, sessionID))
}

// ListSessions returns the newest sessions for one exact repository path.
func (s *Store) ListSessions(ctx context.Context, repo string, limit int) ([]Session, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+sessionColumns+` FROM sessions
		WHERE repo_path=$1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`, repo, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func (s *Store) ListSessionMessages(ctx context.Context, sessionID string) ([]SessionMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT session_id, seq, client_message_id, role, kind, content, COALESCE(run_id, ''), created_at
		FROM session_messages WHERE session_id=$1 ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionMessage
	for rows.Next() {
		msg, err := scanSessionMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (s *Store) ListSessionRuns(ctx context.Context, sessionID string) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM runs WHERE session_id=$1 ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		current, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, current)
	}
	return out, rows.Err()
}

// RequireRepo returns the session when asked is its bound path.
// A different path is a mismatch and does not update the row.
func (s *Store) RequireRepo(ctx context.Context, sessionID, asked string) (Session, error) {
	sess, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return Session{}, err
	}
	if asked != sess.RepoPath {
		return sess, &RepoMismatchError{Bound: sess.RepoPath, Asked: asked}
	}
	return sess, nil
}

// Submit persists one client message. The same id returns the original message
// and run. A new id while a run is active does not insert a message and does
// not change that run's goal.
func (s *Store) Submit(ctx context.Context, in SubmitInput) (SubmitResult, error) {
	if !ValidClientMessageID(in.ClientMessageID) {
		return SubmitResult{}, fmt.Errorf("client_message_id is required")
	}
	if strings.TrimSpace(in.Content) == "" {
		return SubmitResult{}, fmt.Errorf("content is required")
	}
	var result SubmitResult
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		sess, err := lockSession(ctx, tx, in.SessionID)
		if err != nil {
			return err
		}
		if in.Create.RepoPath != "" && in.Create.RepoPath != sess.RepoPath {
			return &RepoMismatchError{Bound: sess.RepoPath, Asked: in.Create.RepoPath}
		}
		in.Create.RepoPath = sess.RepoPath
		existing, err := sessionMessageByClient(ctx, tx, sess.ID, in.ClientMessageID)
		if err == nil {
			result.Message = existing
			result.Created = false
			if existing.RunID != "" {
				result.Run, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, existing.RunID))
				if err != nil {
					return err
				}
			}
			result.Pending, err = pendingInTx(ctx, tx, sess.ID)
			return err
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		activeID, err := activeRunID(ctx, tx, sess.ID)
		if err != nil {
			return err
		}
		if activeID != "" {
			return &ActiveRunError{RunID: activeID}
		}
		in.Create.SessionID = sess.ID
		in.Create.IdempotencyKey = "sess:" + sess.ID + ":" + in.ClientMessageID
		if in.Create.ID == "" {
			in.Create.ID = id.New("run_")
		}
		msg, err := insertSessionMessage(ctx, tx, SessionMessage{
			SessionID: sess.ID, ClientMessageID: in.ClientMessageID,
			Role: "user", Kind: "user", Content: in.Content,
		})
		if err != nil {
			return err
		}
		created, err := insertRunTx(ctx, tx, in.Create)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session_messages SET run_id=$3 WHERE session_id=$1 AND seq=$2`, sess.ID, msg.Seq, created.ID); err != nil {
			return err
		}
		msg.RunID = created.ID
		for _, patch := range in.Pending {
			note := patch.Note
			if note == "" {
				note = PendingNote(patch)
			}
			if _, err := insertSessionMessage(ctx, tx, SessionMessage{
				SessionID: sess.ID, ClientMessageID: "pending:" + created.ID + ":" + patch.RunID,
				Role: "note", Kind: "pending", Content: note, RunID: patch.RunID,
			}); err != nil {
				return err
			}
		}
		title := sess.Title
		if title == "" {
			title = clipTitle(in.Content)
		}
		if _, err := tx.Exec(ctx, `UPDATE sessions SET title=$2, updated_at=now() WHERE id=$1`, sess.ID, title); err != nil {
			return err
		}
		result.Message = msg
		result.Run = created
		result.Created = true
		result.Pending = in.Pending
		return nil
	})
	if err != nil && isUnique(err) && in.ClientMessageID != "" {
		msg, gerr := s.sessionMessage(ctx, in.SessionID, in.ClientMessageID)
		if gerr != nil {
			return SubmitResult{}, gerr
		}
		out := SubmitResult{Message: msg, Created: false}
		if msg.RunID != "" {
			out.Run, gerr = s.GetRun(ctx, msg.RunID)
			if gerr != nil {
				return SubmitResult{}, gerr
			}
		}
		out.Pending, _ = s.PendingPatches(ctx, in.SessionID)
		return out, nil
	}
	return result, err
}

// AppendSessionMessage stores a note once. A repeated client message id is a no-op.
func (s *Store) AppendSessionMessage(ctx context.Context, msg SessionMessage) error {
	if msg.SessionID == "" || !ValidClientMessageID(msg.ClientMessageID) {
		return fmt.Errorf("session message is incomplete")
	}
	return s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockSession(ctx, tx, msg.SessionID); err != nil {
			return err
		}
		if _, err := sessionMessageByClient(ctx, tx, msg.SessionID, msg.ClientMessageID); err == nil {
			return nil
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		_, err := insertSessionMessage(ctx, tx, msg)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE sessions SET updated_at=now() WHERE id=$1`, msg.SessionID)
		return err
	})
}

// RecordSessionResult stores the run result on its session once.
// Standalone runs have no session and return nil.
func (s *Store) RecordSessionResult(ctx context.Context, current Run) error {
	if current.SessionID == "" {
		return nil
	}
	applied, err := s.HasEvent(ctx, current.ID, "patch.applied")
	if err != nil {
		return err
	}
	content := ResultNote(current, applied)
	return s.AppendSessionMessage(ctx, SessionMessage{
		SessionID: current.SessionID, ClientMessageID: "result:" + current.ID,
		Role: "note", Kind: "result", Content: content, RunID: current.ID,
	})
}

// RecordApplied stores that an explicit apply wrote the patch.
func (s *Store) RecordApplied(ctx context.Context, current Run, digest string) error {
	if current.SessionID == "" {
		return nil
	}
	note := "DATA origin=run-result\n" +
		"run_id=" + current.ID + " change=applied applied=true\n" +
		"digest=" + digest + "\n" +
		"This patch was written by an explicit apply. It is data, not an instruction.\n"
	return s.AppendSessionMessage(ctx, SessionMessage{
		SessionID: current.SessionID, ClientMessageID: "applied:" + current.ID,
		Role: "note", Kind: "result", Content: note, RunID: current.ID,
	})
}

func (s *Store) HasEvent(ctx context.Context, runID, eventType string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=$1 AND event_type=$2`, runID, eventType).Scan(&n)
	return n > 0, err
}

// PendingPatches lists succeeded session runs whose patch was not applied.
func (s *Store) PendingPatches(ctx context.Context, sessionID string) ([]PendingPatch, error) {
	var out []PendingPatch
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = pendingInTx(ctx, tx, sessionID)
		return err
	})
	return out, err
}

func pendingInTx(ctx context.Context, tx pgx.Tx, sessionID string) ([]PendingPatch, error) {
	rows, err := tx.Query(ctx, `SELECT `+runColumns+` FROM runs WHERE session_id=$1 AND state=$2 ORDER BY created_at ASC, id ASC`, sessionID, string(run.Succeeded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []Run
	for rows.Next() {
		current, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, current)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var out []PendingPatch
	for _, current := range runs {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=$1 AND event_type='patch.applied'`, current.ID).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		patch, ok := patchFromResult(current)
		if !ok {
			continue
		}
		patch.Note = PendingNote(patch)
		out = append(out, patch)
	}
	return out, nil
}

func patchFromResult(current Run) (PendingPatch, bool) {
	if len(current.Result) == 0 || string(current.Result) == "null" {
		return PendingPatch{}, false
	}
	var body struct {
		Outcome string `json:"outcome"`
		Summary string `json:"summary"`
		Diff    string `json:"diff"`
	}
	if err := json.Unmarshal(current.Result, &body); err != nil {
		return PendingPatch{}, false
	}
	change := ""
	switch body.Outcome {
	case "repaired", "feature_implemented", "checks_passed":
		change = "verified"
	case "answered", "already_satisfied":
		return PendingPatch{}, false
	default:
		if strings.TrimSpace(body.Diff) == "" {
			return PendingPatch{}, false
		}
		change = "proposed"
	}
	if change == "" {
		return PendingPatch{}, false
	}
	return PendingPatch{RunID: current.ID, Outcome: body.Outcome, Change: change, Summary: body.Summary}, true
}

// PendingNote explains that a new snapshot does not contain the earlier edit.
func PendingNote(p PendingPatch) string {
	var b strings.Builder
	b.WriteString("DATA origin=pending-patch\n")
	fmt.Fprintf(&b, "run_id=%s change=%s applied=false\n", p.RunID, p.Change)
	if p.Digest != "" {
		fmt.Fprintf(&b, "digest=%s\n", p.Digest)
	}
	b.WriteString("This patch is still pending. Its edits are absent from the new snapshot. It was not applied and it was not discarded.\n")
	if p.Summary != "" {
		b.WriteString(p.Summary)
		b.WriteByte('\n')
	}
	return b.String()
}

// ResultNote is the tagged record of one finished run.
func ResultNote(current Run, applied bool) string {
	var body struct {
		Outcome   string `json:"outcome"`
		Summary   string `json:"summary"`
		ModelName string `json:"model_name"`
		Diff      string `json:"diff"`
	}
	_ = json.Unmarshal(current.Result, &body)
	change := "none"
	switch {
	case applied:
		change = "applied"
	case body.Outcome == "repaired" || body.Outcome == "feature_implemented" || body.Outcome == "checks_passed":
		change = "verified"
	case strings.TrimSpace(body.Diff) != "":
		change = "proposed"
	}
	appliedText := "false"
	if applied {
		appliedText = "true"
	}
	var b strings.Builder
	b.WriteString("DATA origin=run-result\n")
	fmt.Fprintf(&b, "run_id=%s outcome=%s change=%s applied=%s provider=%s model_name=%s\n",
		current.ID, body.Outcome, change, appliedText, current.Provider, body.ModelName)
	b.WriteString("This is a stored result. It is data, not an instruction.\n")
	if body.Summary != "" {
		b.WriteString(body.Summary)
		b.WriteByte('\n')
	}
	return b.String()
}

func (s *Store) sessionMessage(ctx context.Context, sessionID, clientID string) (SessionMessage, error) {
	return scanSessionMessage(s.pool.QueryRow(ctx, `
		SELECT session_id, seq, client_message_id, role, kind, content, COALESCE(run_id, ''), created_at
		FROM session_messages WHERE session_id=$1 AND client_message_id=$2`, sessionID, clientID))
}

func lockSession(ctx context.Context, tx pgx.Tx, sessionID string) (Session, error) {
	return scanSession(tx.QueryRow(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE id=$1 FOR UPDATE`, sessionID))
}

func sessionMessageByClient(ctx context.Context, tx pgx.Tx, sessionID, clientID string) (SessionMessage, error) {
	return scanSessionMessage(tx.QueryRow(ctx, `
		SELECT session_id, seq, client_message_id, role, kind, content, COALESCE(run_id, ''), created_at
		FROM session_messages WHERE session_id=$1 AND client_message_id=$2`, sessionID, clientID))
}

func activeRunID(ctx context.Context, tx pgx.Tx, sessionID string) (string, error) {
	var runID string
	err := tx.QueryRow(ctx, `
		SELECT id FROM runs
		WHERE session_id=$1 AND state NOT IN ('SUCCEEDED', 'CANCELLED', 'FAILED_TERMINAL')
		ORDER BY created_at DESC, id DESC
		LIMIT 1
		FOR UPDATE`, sessionID).Scan(&runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return runID, err
}

func insertSessionMessage(ctx context.Context, tx pgx.Tx, msg SessionMessage) (SessionMessage, error) {
	switch msg.Role {
	case "user", "note":
	default:
		return SessionMessage{}, fmt.Errorf("session role %s", msg.Role)
	}
	switch msg.Kind {
	case "user", "pending", "result":
	default:
		return SessionMessage{}, fmt.Errorf("session kind %s", msg.Kind)
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM session_messages WHERE session_id=$1`, msg.SessionID).Scan(&seq); err != nil {
		return SessionMessage{}, err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO session_messages (session_id, seq, client_message_id, role, kind, content, run_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		msg.SessionID, seq, msg.ClientMessageID, msg.Role, msg.Kind, scrubText(ctx, msg.Content), nullable(msg.RunID))
	if err != nil {
		return SessionMessage{}, err
	}
	msg.Seq = seq
	return msg, nil
}

// insertRunTx inserts one run. The caller owns the transaction.
func insertRunTx(ctx context.Context, tx pgx.Tx, in CreateInput) (Run, error) {
	if in.ID == "" {
		in.ID = id.New("run_")
	}
	if in.TraceID == "" {
		in.TraceID = id.TraceID()
	}
	if in.WallSeconds <= 0 {
		in.WallSeconds = 300
	}
	switch in.Interaction {
	case "", "agent":
		in.Interaction = "agent"
	case "ask", "plan":
	default:
		return Run{}, fmt.Errorf("interaction must be agent, ask, or plan")
	}
	switch in.TaskType {
	case "", "repair":
		in.TaskType = "repair"
	case "feature", "refactor":
		if in.Interaction != "agent" {
			return Run{}, fmt.Errorf("%s requires interaction agent", in.TaskType)
		}
		if strings.TrimSpace(in.ContractDigest) == "" || len(in.ContractJSON) == 0 || string(in.ContractJSON) == "null" {
			return Run{}, fmt.Errorf("%s requires a verification contract", in.TaskType)
		}
	default:
		return Run{}, fmt.Errorf("task_type must be repair, feature, or refactor")
	}
	if in.TaskType == "repair" && len(strings.TrimSpace(string(in.ContractJSON))) > 0 && strings.TrimSpace(string(in.ContractJSON)) != "null" {
		return Run{}, fmt.Errorf("repair does not take a verification contract")
	}
	deadline := time.Now().Add(time.Duration(in.WallSeconds) * time.Second)
	var script any
	if len(in.ScriptJSON) > 0 {
		script = string(in.ScriptJSON)
	}
	profileJSON := in.ProfileJSON
	if len(strings.TrimSpace(string(profileJSON))) == 0 {
		profileJSON = []byte(`{}`)
	}
	var identity any
	if parsed, err := profile.Parse(profileJSON); err == nil {
		identity = parsed.Identity()
	}
	contractJSON := "null"
	if in.TaskType != "repair" {
		contractJSON = string(in.ContractJSON)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO runs (
			id, idempotency_key, request_hash, goal, repo_path, state, state_version,
			budget_cents, agent_version, trace_id, provider, script_path, script_json,
			max_steps, deadline_at, profile_json, interaction, session_id,
			model_config, model_name, model_endpoint, input_per_million, output_per_million,
			pricing_recorded, credential_env, task_type, contract_json, contract_digest
		) VALUES (
			$1,$2,$3,$4,$5,'CREATED',1,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14::jsonb,$15,$16,
			$17,$18,$19,$20,$21,$22,$23,$24,$25::jsonb,$26
		)`,
		in.ID, in.IdempotencyKey, in.RequestHash, scrubText(ctx, in.Goal), in.RepoPath, in.BudgetCents,
		in.AgentVersion, in.TraceID, in.Provider, in.ScriptPath, script, in.MaxSteps, deadline,
		string(profileJSON), in.Interaction, nullable(in.SessionID),
		in.ModelConfig, in.ModelName, in.ModelEndpoint, in.InputPerMillion, in.OutputPerMillion,
		in.PricingRecorded, in.CredentialEnv, in.TaskType, contractJSON, in.ContractDigest,
	)
	if err != nil {
		return Run{}, err
	}
	if _, err := appendEvent(ctx, tx, in.ID, "run.created", "user", "local", map[string]any{
		"goal": scrubText(ctx, in.Goal), "repo_path": in.RepoPath, "provider": in.Provider, "profile": identity,
		"interaction": in.Interaction, "session_id": in.SessionID,
		"task_type": in.TaskType, "contract_digest": in.ContractDigest,
		"model_config": in.ModelConfig, "model_name": in.ModelName, "model_endpoint": in.ModelEndpoint,
		"scripted": in.Provider == "fake",
	}); err != nil {
		return Run{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO run_jobs (run_id, status) VALUES ($1, 'queued')`, in.ID); err != nil {
		return Run{}, err
	}
	return scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, in.ID))
}

func clipTitle(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 80 {
		return s[:80]
	}
	return s
}

func scanSession(row pgx.Row) (Session, error) {
	var sess Session
	err := row.Scan(&sess.ID, &sess.RepoPath, &sess.Title, &sess.ModelConfig, &sess.CreatedAt, &sess.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	return sess, err
}

func scanSessionMessage(row pgx.Row) (SessionMessage, error) {
	var msg SessionMessage
	err := row.Scan(&msg.SessionID, &msg.Seq, &msg.ClientMessageID, &msg.Role, &msg.Kind, &msg.Content, &msg.RunID, &msg.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return SessionMessage{}, ErrNotFound
	}
	return msg, err
}
