package store

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/csy20/agt/internal/diag"
	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/profile"
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
error, step_count, max_steps, deadline_at, created_at, updated_at, profile_json`

type Store struct {
	pool *pgxpool.Pool
}

type Run struct {
	ID             string
	IdempotencyKey string
	RequestHash    string
	Goal           string
	RepoPath       string
	Worktree       string
	State          run.State
	StateVersion   int64
	RiskLevel      string
	BudgetCents    int
	SpentMicros    int64
	AgentVersion   string
	TraceID        string
	Provider       string
	ScriptPath     string
	ScriptJSON     json.RawMessage
	Plan           json.RawMessage
	Result         json.RawMessage
	Error          string
	StepCount      int
	MaxSteps       int
	Deadline       time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	ProfileJSON    json.RawMessage
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
	ID             string
	IdempotencyKey string
	RequestHash    string
	Goal           string
	RepoPath       string
	BudgetCents    int
	MaxSteps       int
	WallSeconds    int
	AgentVersion   string
	TraceID        string
	Provider       string
	ScriptPath     string
	ScriptJSON     json.RawMessage
	ProfileJSON    json.RawMessage
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
	return s.applyMigration(ctx, 2, "migrations/002_profile.sql")
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
	return s.withTx(ctx, func(tx pgx.Tx) error {
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
	_, err := s.pool.Exec(ctx, `
		TRUNCATE usage_entries, approvals, commands, run_events, run_jobs, runs
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
	deadline := time.Now().Add(time.Duration(in.WallSeconds) * time.Second)
	var script any
	if len(in.ScriptJSON) > 0 {
		script = string(in.ScriptJSON)
	}
	profileJSON := in.ProfileJSON
	if len(bytes.TrimSpace(profileJSON)) == 0 {
		profileJSON = []byte(`{}`)
	}
	var identity any
	if parsed, err := profile.Parse(profileJSON); err == nil {
		identity = parsed.Identity()
	}
	var out Run
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO runs (
				id, idempotency_key, request_hash, goal, repo_path, state, state_version,
				budget_cents, agent_version, trace_id, provider, script_path, script_json,
				max_steps, deadline_at, profile_json
			) VALUES (
				$1,$2,$3,$4,$5,'CREATED',1,$6,$7,$8,$9,$10,$11::jsonb,$12,$13,$14::jsonb
			)`,
			in.ID, in.IdempotencyKey, in.RequestHash, in.Goal, in.RepoPath, in.BudgetCents,
			in.AgentVersion, in.TraceID, in.Provider, in.ScriptPath, script, in.MaxSteps, deadline,
			string(profileJSON),
		)
		if err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, in.ID, "run.created", "user", "local", map[string]any{
			"goal": in.Goal, "repo_path": in.RepoPath, "provider": in.Provider, "profile": identity,
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO run_jobs (run_id, status) VALUES ($1, 'queued')`, in.ID); err != nil {
			return err
		}
		out, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, in.ID))
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

func (s *Store) getByIdempotency(ctx context.Context, key string) (Run, error) {
	return scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE idempotency_key=$1`, key))
}

func (s *Store) Transition(ctx context.Context, runID string, fromVersion int64, to run.State, actor string) (Run, error) {
	var out Run
	err := s.withTx(ctx, func(tx pgx.Tx) error {
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
	err := s.withTx(ctx, func(tx pgx.Tx) error {
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

func (s *Store) SetResult(ctx context.Context, runID string, result any, errText string) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
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

func (s *Store) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockRun(ctx context.Context, tx pgx.Tx, runID string) (Run, error) {
	return scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1 FOR UPDATE`, runID))
}

func scanRun(row pgx.Row) (Run, error) {
	var r Run
	var state string
	var script, plan, result, profileJSON []byte
	err := row.Scan(
		&r.ID, &r.IdempotencyKey, &r.RequestHash, &r.Goal, &r.RepoPath, &r.Worktree,
		&state, &r.StateVersion, &r.RiskLevel, &r.BudgetCents, &r.SpentMicros,
		&r.AgentVersion, &r.TraceID, &r.Provider, &r.ScriptPath,
		&script, &plan, &result, &r.Error, &r.StepCount, &r.MaxSteps,
		&r.Deadline, &r.CreatedAt, &r.UpdatedAt, &profileJSON,
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
	return r, nil
}

func appendEvent(ctx context.Context, tx pgx.Tx, runID, typ, actorType, actorID string, payload any) (int64, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
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
