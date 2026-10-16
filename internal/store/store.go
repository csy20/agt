package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/csy20/agt/internal/diag"
	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/run"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const runColumns = `
id, idempotency_key, request_hash, goal, repo_path, worktree, state, state_version,
risk_level, budget_cents, spent_micros, agent_version, trace_id, provider, script_path,
COALESCE(script_json, 'null'::jsonb), COALESCE(plan_json, 'null'::jsonb), COALESCE(result_json, 'null'::jsonb),
error, step_count, max_steps, deadline_at, created_at, updated_at, profile_json, interaction,
COALESCE(session_id, ''), model_config, model_name, model_endpoint, input_per_million,
output_per_million, pricing_recorded, credential_env, task_type,
COALESCE(contract_json, 'null'::jsonb), contract_digest,
COALESCE(baseline_json, 'null'::jsonb), COALESCE(final_json, 'null'::jsonb),
verification_outcome, verification_digest`

type Store struct {
	pool *pgxpool.Pool
	// Redact removes credential values before they are stored in a transcript,
	// an event, or a result. Nil leaves text unchanged.
	Redact func(string) string
	// resetToken is empty on a production store. Reset runs only when this
	// token matches the harness row in a disposable database.
	resetToken string
}

type Run struct {
	ID                  string
	IdempotencyKey      string
	RequestHash         string
	Goal                string
	RepoPath            string
	Worktree            string
	State               run.State
	StateVersion        int64
	RiskLevel           string
	BudgetCents         int
	SpentMicros         int64
	AgentVersion        string
	TraceID             string
	Provider            string
	ScriptPath          string
	ScriptJSON          json.RawMessage
	Plan                json.RawMessage
	Result              json.RawMessage
	Error               string
	StepCount           int
	MaxSteps            int
	Deadline            time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
	ProfileJSON         json.RawMessage
	Interaction         string
	SessionID           string
	ModelConfig         string
	ModelName           string
	ModelEndpoint       string
	InputPerMillion     int64
	OutputPerMillion    int64
	PricingRecorded     bool
	CredentialEnv       string
	TaskType            string
	ContractJSON        json.RawMessage
	ContractDigest      string
	BaselineJSON        json.RawMessage
	FinalJSON           json.RawMessage
	VerificationOutcome string
	VerificationDigest  string
}

type Event struct {
	Seq       int64           `json:"sequence_no"`
	Type      string          `json:"event_type"`
	ActorType string          `json:"actor_type"`
	ActorID   string          `json:"actor_id"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type CreateInput struct {
	ID               string
	IdempotencyKey   string
	RequestHash      string
	Goal             string
	RepoPath         string
	BudgetCents      int
	MaxSteps         int
	WallSeconds      int
	AgentVersion     string
	TraceID          string
	Provider         string
	ScriptPath       string
	ScriptJSON       json.RawMessage
	ProfileJSON      json.RawMessage
	Interaction      string
	SessionID        string
	ModelConfig      string
	ModelName        string
	ModelEndpoint    string
	InputPerMillion  int64
	OutputPerMillion int64
	PricingRecorded  bool
	CredentialEnv    string
	TaskType         string
	ContractJSON     json.RawMessage
	ContractDigest   string
}

func Open(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, diag.PostgresUnreachable(url, err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	if err := s.applyMigration(ctx, 1, "migrations/001_init.sql"); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, 2, "migrations/002_profile.sql"); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, 3, "migrations/003_transcript.sql"); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, 4, "migrations/004_lease.sql"); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, 5, "migrations/005_interaction.sql"); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, 6, "migrations/006_sessions.sql"); err != nil {
		return err
	}
	if err := s.applyMigration(ctx, 7, "migrations/007_models.sql"); err != nil {
		return err
	}
	return s.applyMigration(ctx, 8, "migrations/008_verify.sql")
}

func (s *Store) applyMigration(ctx context.Context, version int, name string) error {
	applied, err := s.migrationApplied(ctx, version)
	if err != nil || applied {
		return err
	}
	sqlBytes, err := migrationFS.ReadFile(name)
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		// 8675310 serializes migrations inside this database only.
		if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '30s'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(8675310)`); err != nil {
			return err
		}
		var reg *string
		if err := tx.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations')`).Scan(&reg); err != nil {
			return err
		}
		if reg != nil {
			var n int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=$1`, version).Scan(&n); err != nil {
				return err
			}
			if n > 0 {
				return nil
			}
		}
		for _, stmt := range splitSQL(string(sqlBytes)) {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("migration %d: %w", version, err)
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version)
		return err
	})
}

func splitSQL(sql string) []string {
	parts := strings.Split(sql, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		stmt := strings.TrimSpace(part)
		if stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

func (s *Store) migrationApplied(ctx context.Context, version int) (bool, error) {
	var name *string
	err := s.pool.QueryRow(ctx, `SELECT to_regclass('public.schema_migrations')`).Scan(&name)
	if err != nil {
		return false, err
	}
	if name == nil {
		return false, nil
	}
	var n int
	err = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=$1`, version).Scan(&n)
	return n > 0, err
}

func (s *Store) Reset(ctx context.Context) error {
	// An unarmed store must not query or truncate. Production never sets the token.
	if strings.TrimSpace(s.resetToken) == "" {
		return fmt.Errorf("reset is not armed on this store")
	}
	var name string
	if err := s.pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil {
		return err
	}
	var marker, purpose string
	if err := s.pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT value FROM agt_harness WHERE key = 'token'), ''),
			COALESCE((SELECT value FROM agt_harness WHERE key = 'purpose'), '')`).Scan(&marker, &purpose); err != nil {
		return fmt.Errorf("reset refused: %w", err)
	}
	if err := AuthorizeReset(s.resetToken, name, marker, purpose); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		TRUNCATE model_messages, usage_entries, approvals, commands, run_events, run_jobs,
			session_messages, runs, sessions
		RESTART IDENTITY CASCADE`)
	return err
}

func (s *Store) CreateRun(ctx context.Context, in CreateInput) (Run, bool, error) {
	if in.IdempotencyKey != "" {
		existing, err := s.getByIdempotency(ctx, in.IdempotencyKey)
		if err == nil {
			if existing.RequestHash != in.RequestHash {
				return Run{}, false, ErrIdempotency
			}
			return existing, false, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return Run{}, false, err
		}
	}
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
		return Run{}, false, fmt.Errorf("interaction must be agent, ask, or plan")
	}
	var out Run
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = insertRunTx(ctx, tx, in)
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && in.IdempotencyKey != "" {
			existing, gerr := s.getByIdempotency(ctx, in.IdempotencyKey)
			if gerr != nil {
				return Run{}, false, gerr
			}
			if existing.RequestHash != in.RequestHash {
				return Run{}, false, ErrIdempotency
			}
			return existing, false, nil
		}
		return Run{}, false, err
	}
	return out, true, nil
}

func (s *Store) GetRun(ctx context.Context, runID string) (Run, error) {
	return scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, runID))
}

// ListRuns returns the newest runs, up to limit. limit is clamped to 1..100.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM runs ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Run{}
	for rows.Next() {
		current, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, current)
	}
	return out, rows.Err()
}

func (s *Store) getByIdempotency(ctx context.Context, key string) (Run, error) {
	return scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE idempotency_key=$1`, key))
}

func (s *Store) Transition(ctx context.Context, runID string, fromVersion int64, to run.State, actor string) (Run, error) {
	var out Run
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := lockRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if cur.StateVersion != fromVersion {
			return ErrConflict
		}
		if !run.Can(cur.State, to) {
			return fmt.Errorf("illegal transition %s -> %s", cur.State, to)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE runs
			SET state=$2, state_version=state_version+1, updated_at=now()
			WHERE id=$1 AND state_version=$3`, runID, string(to), fromVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if _, err := appendEvent(ctx, tx, runID, "state.changed", "system", actor, map[string]any{
			"from": cur.State, "to": to,
		}); err != nil {
			return err
		}
		out, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, runID))
		return err
	})
	return out, err
}

func (s *Store) AppendEvent(ctx context.Context, runID, typ, actorType, actorID string, payload any) (int64, error) {
	var seq int64
	err := s.withTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := lockRun(ctx, tx, runID); err != nil {
			return err
		}
		var err error
		seq, err = appendEvent(ctx, tx, runID, typ, actorType, actorID, payload)
		return err
	})
	return seq, err
}

func (s *Store) EventsAfter(ctx context.Context, runID string, after int64) ([]Event, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sequence_no, event_type, actor_type, actor_id, payload, created_at
		FROM run_events
		WHERE run_id=$1 AND sequence_no>$2
		ORDER BY sequence_no ASC`, runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var ev Event
		if err := rows.Scan(&ev.Seq, &ev.Type, &ev.ActorType, &ev.ActorID, &ev.Payload, &ev.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

func (s *Store) CountEvents(ctx context.Context, runID, typ string) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM run_events WHERE run_id=$1 AND event_type=$2`, runID, typ).Scan(&n)
	return n, err
}

func (s *Store) MaxSeq(ctx context.Context, runID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(sequence_no), 0) FROM run_events WHERE run_id=$1`, runID).Scan(&n)
	return n, err
}

func (s *Store) SetWorktree(ctx context.Context, runID, path string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET worktree=$2, updated_at=now() WHERE id=$1`, runID, path)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

// AppendDisplay records assistant text for the session to show while the
// model response is still being read. It does not execute a tool.
func (s *Store) AppendDisplay(ctx context.Context, runID, text string) error {
	if text == "" {
		return nil
	}
	_, err := s.AppendEvent(ctx, runID, "assistant.delta", "model", "display", map[string]any{"text": text})
	return err
}

// SetBaseline records the pre-edit check results once.
// A second call does not replace them. The contract columns are not written.
func (s *Store) SetBaseline(ctx context.Context, runID string, baseline []byte) error {
	if len(bytesTrim(baseline)) == 0 {
		return fmt.Errorf("baseline is empty")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs SET baseline_json=$2::jsonb, updated_at=now()
		WHERE id=$1 AND baseline_json = 'null'::jsonb`, runID, string(baseline))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM runs WHERE id=$1`, runID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return fmt.Errorf("baseline is already recorded")
}

// SetVerification records the latest verifier results and the artifact digest
// they apply to. It does not update contract_json or contract_digest.
func (s *Store) SetVerification(ctx context.Context, runID string, final []byte, outcome, digest string) error {
	if len(bytesTrim(final)) == 0 {
		final = []byte("null")
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE runs
		SET final_json=$2::jsonb, verification_outcome=$3, verification_digest=$4, updated_at=now()
		WHERE id=$1`, runID, string(final), outcome, digest)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func bytesTrim(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func (s *Store) SetResult(ctx context.Context, runID string, result any, errText string) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	errText = scrubText(context.WithValue(ctx, scrubKey{}, s.Redact), errText)
	body = []byte(scrubText(context.WithValue(ctx, scrubKey{}, s.Redact), string(body)))
	tag, err := s.pool.Exec(ctx, `UPDATE runs SET result_json=$2::jsonb, error=$3, updated_at=now() WHERE id=$1`, runID, string(body), errText)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UsageTotal(ctx context.Context, runID string) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(cost_micros), 0) FROM usage_entries WHERE run_id=$1`, runID).Scan(&n)
	return n, err
}

type scrubKey struct{}

func (s *Store) withTx(ctx context.Context, fn func(context.Context, pgx.Tx) error) error {
	if s != nil && s.Redact != nil {
		ctx = context.WithValue(ctx, scrubKey{}, s.Redact)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func scrubText(ctx context.Context, s string) string {
	clean, _ := ctx.Value(scrubKey{}).(func(string) string)
	if clean == nil || s == "" {
		return s
	}
	return clean(s)
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func lockRun(ctx context.Context, tx pgx.Tx, runID string) (Run, error) {
	return scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1 FOR UPDATE`, runID))
}

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var state string
	var script, plan, result, profileJSON, contractJSON, baselineJSON, finalJSON []byte
	err := row.Scan(
		&r.ID, &r.IdempotencyKey, &r.RequestHash, &r.Goal, &r.RepoPath, &r.Worktree,
		&state, &r.StateVersion, &r.RiskLevel, &r.BudgetCents, &r.SpentMicros,
		&r.AgentVersion, &r.TraceID, &r.Provider, &r.ScriptPath,
		&script, &plan, &result, &r.Error, &r.StepCount, &r.MaxSteps,
		&r.Deadline, &r.CreatedAt, &r.UpdatedAt, &profileJSON, &r.Interaction, &r.SessionID,
		&r.ModelConfig, &r.ModelName, &r.ModelEndpoint, &r.InputPerMillion, &r.OutputPerMillion,
		&r.PricingRecorded, &r.CredentialEnv, &r.TaskType, &contractJSON, &r.ContractDigest,
		&baselineJSON, &finalJSON, &r.VerificationOutcome, &r.VerificationDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	r.State = run.State(state)
	r.ScriptJSON = script
	r.Plan = plan
	r.Result = result
	r.ProfileJSON = profileJSON
	r.ContractJSON = contractJSON
	r.BaselineJSON = baselineJSON
	r.FinalJSON = finalJSON
	if r.TaskType == "" {
		r.TaskType = "repair"
	}
	return r, nil
}

func appendEvent(ctx context.Context, tx pgx.Tx, runID, typ, actorType, actorID string, payload any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	body = []byte(scrubText(ctx, string(body)))
	var seq int64
	err = tx.QueryRow(ctx, `
		INSERT INTO run_events (run_id, sequence_no, event_type, actor_type, actor_id, payload)
		VALUES (
			$1,
			(SELECT COALESCE(MAX(sequence_no), 0) + 1 FROM run_events WHERE run_id=$1),
			$2, $3, $4, $5::jsonb
		)
		RETURNING sequence_no`, runID, typ, actorType, actorID, string(body)).Scan(&seq)
	return seq, err
}
