package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/csy20/agt/internal/redact"
	"github.com/csy20/agt/internal/run"
	"github.com/jackc/pgx/v5"
)

type Command struct {
	CommandID      string          `json:"command_id"`
	RunID          string          `json:"run_id"`
	Tool           string          `json:"tool"`
	Canonical      json.RawMessage `json:"canonical_args"`
	ArgsHash       string          `json:"args_hash"`
	PolicyDecision string          `json:"policy_decision"`
	ApprovalID     string          `json:"approval_id,omitempty"`
	State          string          `json:"state"`
	Result         json.RawMessage `json:"result,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type Intent struct {
	Kind         string
	Plan         json.RawMessage
	Tool         string
	Canonical    json.RawMessage
	ArgsHash     string
	Decision     string
	Reason       string
	CommandID    string
	Text         string
	InputTokens  int
	OutputTokens int
	CostMicros   int64
	Model        string
}

type Approval struct {
	ID        string
	RunID     string
	CommandID string
	ArgsHash  string
	Approver  string
	Decision  string
}

type Job struct {
	ID    int64
	RunID string
}

func (s *Store) CommitModelIntent(ctx context.Context, runID string, fromVersion int64, in Intent) (Run, error) {
	var out Run
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockRun(ctx, tx, runID)
		if err != nil {
			return err
		}
		if cur.StateVersion != fromVersion {
			return ErrConflict
		}
		if cur.State != run.Planning && cur.State != run.Observing {
			return fmt.Errorf("cannot accept model output from %s", cur.State)
		}
		next := cur.State
		switch in.Kind {
		case "budget":
			next = run.PausedBudget
		case "finish":
			next = run.Verifying
		case "plan":
			if cur.State == run.Observing {
				next = run.Planning
			}
		case "tool":
			switch in.Decision {
			case "deny":
				next = run.Observing
			case "require_approval":
				next = run.AwaitingApproval
			default:
				next = run.Executing
			}
		default:
			return fmt.Errorf("unknown intent kind %s", in.Kind)
		}
		if next != cur.State && !run.Can(cur.State, next) {
			return fmt.Errorf("illegal transition %s -> %s", cur.State, next)
		}
		if err := insertUsage(ctx, tx, runID, "model.call", int64(in.InputTokens+in.OutputTokens), in.CostMicros); err != nil {
			return err
		}
		if in.InputTokens > 0 {
			if err := insertUsage(ctx, tx, runID, "model.input_tokens", int64(in.InputTokens), 0); err != nil {
				return err
			}
		}
		if in.OutputTokens > 0 {
			if err := insertUsage(ctx, tx, runID, "model.output_tokens", int64(in.OutputTokens), 0); err != nil {
				return err
			}
		}
		if len(in.Plan) > 0 && string(in.Plan) != "null" {
			if _, err := tx.Exec(ctx, `UPDATE runs SET plan_json=$2::jsonb WHERE id=$1`, runID, string(in.Plan)); err != nil {
				return err
			}
			if _, err := appendEvent(ctx, tx, runID, "plan.updated", "model", nonempty(in.Model, "model"), map[string]any{
				"plan": json.RawMessage(in.Plan),
			}); err != nil {
				return err
			}
		}
		if _, err := appendEvent(ctx, tx, runID, "model.called", "model", nonempty(in.Model, "model"), map[string]any{
			"kind": in.Kind, "tool": in.Tool, "input_tokens": in.InputTokens,
			"output_tokens": in.OutputTokens, "cost_micros": in.CostMicros, "text": in.Text,
		}); err != nil {
			return err
		}
		if in.Kind == "tool" {
			if err := insertIntentCommand(ctx, tx, runID, in); err != nil {
				return err
			}
		}
		if in.Kind == "budget" {
			if _, err := appendEvent(ctx, tx, runID, "budget.paused", "system", "budget", map[string]any{
				"spent_micros": cur.SpentMicros + in.CostMicros, "budget_cents": cur.BudgetCents,
			}); err != nil {
				return err
			}
		}
		spent := cur.SpentMicros + in.CostMicros
		step := cur.StepCount + 1
		if next != cur.State {
			tag, err := tx.Exec(ctx, `
				UPDATE runs
				SET state=$2, state_version=state_version+1, spent_micros=$3, step_count=$4, updated_at=now()
				WHERE id=$1 AND state_version=$5`, runID, string(next), spent, step, fromVersion)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
			if _, err := appendEvent(ctx, tx, runID, "state.changed", "system", "orchestrator", map[string]any{
				"from": cur.State, "to": next,
			}); err != nil {
				return err
			}
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE runs SET spent_micros=$2, step_count=$3, updated_at=now()
				WHERE id=$1 AND state_version=$4`, runID, spent, step, fromVersion)
			if err != nil {
				return err
			}
			if tag.RowsAffected() != 1 {
				return ErrConflict
			}
		}
		out, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, runID))
		return err
	})
	return out, err
}

func insertIntentCommand(ctx context.Context, tx pgx.Tx, runID string, in Intent) error {
	state := "pending"
	var result any
	eventType := "command.started"
	payload := map[string]any{
		"command_id": in.CommandID, "tool": in.Tool, "decision": in.Decision,
		"args_hash": in.ArgsHash, "reason": in.Reason,
	}
	switch in.Decision {
	case "deny":
		state = "denied"
		body, _ := json.Marshal(map[string]any{"exit_code": 2, "output": in.Reason})
		result = string(body)
		eventType = "command.finished"
		payload["exit_code"] = 2
		payload["output_excerpt"] = clip(redact.Text(in.Reason), 500)
	case "require_approval":
		state = "awaiting_approval"
		eventType = "approval.requested"
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO commands (
			command_id, run_id, tool, canonical_args, args_hash, policy_decision, state, result
		) VALUES ($1,$2,$3,$4::jsonb,$5,$6,$7,$8::jsonb)`,
		in.CommandID, runID, in.Tool, string(in.Canonical), in.ArgsHash, in.Decision, state, result,
	); err != nil {
		return err
	}
	if _, err := appendEvent(ctx, tx, runID, "policy.decided", "policy", "engine", payload); err != nil {
		return err
	}
	_, err := appendEvent(ctx, tx, runID, eventType, "policy", "engine", payload)
	return err
}

func (s *Store) OpenCommand(ctx context.Context, runID string) (Command, error) {
	return scanCommand(s.pool.QueryRow(ctx, `
		SELECT `+commandColumns+`
		FROM commands
		WHERE run_id=$1 AND state IN ('pending', 'approved', 'awaiting_approval')
		ORDER BY created_at ASC
		LIMIT 1`, runID))
}

func (s *Store) GetCommand(ctx context.Context, runID, commandID string) (Command, error) {
	return scanCommand(s.pool.QueryRow(ctx, `
		SELECT `+commandColumns+` FROM commands WHERE run_id=$1 AND command_id=$2`, runID, commandID))
}

func (s *Store) ListCommands(ctx context.Context, runID string) ([]Command, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+commandColumns+` FROM commands WHERE run_id=$1 ORDER BY created_at ASC, command_id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Command
	for rows.Next() {
		cmd, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, cmd)
	}
	return out, rows.Err()
}

func (s *Store) FinishCommand(ctx context.Context, commandID, state string, result any) error {
	body, err := json.Marshal(result)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE commands
		SET state=$2, result=$3::jsonb, updated_at=now()
		WHERE command_id=$1 AND state IN ('pending', 'approved')`, commandID, state, string(body))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (s *Store) RecordSystemCommand(ctx context.Context, runID, commandID, tool, argsHash string, canonical []byte, output string, exitCode int) error {
	body, err := json.Marshal(map[string]any{"exit_code": exitCode, "output": output})
	if err != nil {
		return err
	}
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := lockRun(ctx, tx, runID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO commands (
				command_id, run_id, tool, canonical_args, args_hash, policy_decision, state, result
			) VALUES ($1,$2,$3,$4::jsonb,$5,'allow','succeeded',$6::jsonb)`,
			commandID, runID, tool, string(canonical), argsHash, string(body),
		); err != nil {
			return err
		}
		if err := insertUsage(ctx, tx, runID, "sandbox.test_runs", 1, 0); err != nil {
			return err
		}
		_, err := appendEvent(ctx, tx, runID, "command.finished", "system", "verifier", map[string]any{
			"command_id": commandID, "tool": tool, "decision": "allow", "args_hash": argsHash,
			"exit_code": exitCode, "output_excerpt": clip(redact.Text(output), 500),
		})
		return err
	})
}

func (s *Store) Approve(ctx context.Context, in Approval) (Run, error) {
	if in.Decision != "approved" && in.Decision != "rejected" {
		return Run{}, fmt.Errorf("decision must be approved or rejected")
	}
	var out Run
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		cur, err := lockRun(ctx, tx, in.RunID)
		if err != nil {
			return err
		}
		cmd, err := scanCommand(tx.QueryRow(ctx, `
			SELECT `+commandColumns+` FROM commands WHERE command_id=$1 FOR UPDATE`, in.CommandID))
		if err != nil {
			return err
		}
		if cmd.RunID != in.RunID {
			return ErrNotFound
		}
		if cmd.ArgsHash != in.ArgsHash {
			return ErrHashMismatch
		}
		if cmd.State != "awaiting_approval" {
			return ErrConflict
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO approvals (id, run_id, command_id, args_hash, approver, decision)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			in.ID, in.RunID, in.CommandID, in.ArgsHash, in.Approver, in.Decision,
		); err != nil {
			return err
		}
		nextCmd := "approved"
		nextRun := run.Executing
		if in.Decision == "rejected" {
			nextCmd = "denied"
			nextRun = run.Cancelled
		}
		if _, err := tx.Exec(ctx, `
			UPDATE commands SET state=$2, approval_id=$3, updated_at=now() WHERE command_id=$1`,
			in.CommandID, nextCmd, in.ID,
		); err != nil {
			return err
		}
		if !run.Can(cur.State, nextRun) {
			return fmt.Errorf("illegal transition %s -> %s", cur.State, nextRun)
		}
		tag, err := tx.Exec(ctx, `
			UPDATE runs SET state=$2, state_version=state_version+1, updated_at=now()
			WHERE id=$1 AND state_version=$3`, in.RunID, string(nextRun), cur.StateVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConflict
		}
		if nextRun == run.Executing {
			if _, err := tx.Exec(ctx, `INSERT INTO run_jobs (run_id, status) VALUES ($1, 'queued')`, in.RunID); err != nil {
				return err
			}
		}
		if _, err := appendEvent(ctx, tx, in.RunID, "approval.recorded", "user", in.Approver, map[string]any{
			"command_id": in.CommandID, "args_hash": in.ArgsHash, "decision": in.Decision,
		}); err != nil {
			return err
		}
		if _, err := appendEvent(ctx, tx, in.RunID, "state.changed", "system", "approval", map[string]any{
			"from": cur.State, "to": nextRun,
		}); err != nil {
			return err
		}
		out, err = scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM runs WHERE id=$1`, in.RunID))
		return err
	})
	return out, err
}

func (s *Store) Enqueue(ctx context.Context, runID string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO run_jobs (run_id, status) VALUES ($1, 'queued')`, runID)
	return err
}

func (s *Store) ClaimJob(ctx context.Context, owner string) (Job, error) {
	var job Job
	err := s.pool.QueryRow(ctx, `
		WITH next AS (
			SELECT id FROM run_jobs
			WHERE status = 'queued'
			   OR (status = 'claimed' AND lease_until < now())
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		UPDATE run_jobs AS j
		SET status = 'claimed', lease_owner = $1, lease_until = now() + interval '30 seconds'
		FROM next
		WHERE j.id = next.id
		RETURNING j.id, j.run_id`, owner).Scan(&job.ID, &job.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	return job, err
}

func (s *Store) FinishJob(ctx context.Context, jobID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE run_jobs SET status='done', lease_until=NULL WHERE id=$1`, jobID)
	return err
}

func (s *Store) ReleaseJob(ctx context.Context, jobID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE run_jobs
		SET status='queued', lease_owner='', lease_until=NULL
		WHERE id=$1 AND status='claimed'`, jobID)
	return err
}

const commandColumns = `
command_id, run_id, tool, canonical_args, args_hash, policy_decision,
COALESCE(approval_id, ''), state, COALESCE(result, 'null'::jsonb), created_at, updated_at`

func scanCommand(row pgx.Row) (Command, error) {
	var cmd Command
	var canonical, result []byte
	err := row.Scan(
		&cmd.CommandID, &cmd.RunID, &cmd.Tool, &canonical, &cmd.ArgsHash, &cmd.PolicyDecision,
		&cmd.ApprovalID, &cmd.State, &result, &cmd.CreatedAt, &cmd.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Command{}, ErrNotFound
	}
	if err != nil {
		return Command{}, err
	}
	cmd.Canonical = canonical
	cmd.Result = result
	return cmd, nil
}

func insertUsage(ctx context.Context, tx pgx.Tx, runID, kind string, quantity, cost int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO usage_entries (run_id, kind, quantity, cost_micros)
		VALUES ($1,$2,$3,$4)`, runID, kind, quantity, cost)
	return err
}

func nonempty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
