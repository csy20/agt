package store

import (
	"context"

	"github.com/csy20/agt/internal/model"
	"github.com/jackc/pgx/v5"
)

// TranscriptMessage is one row of transcript.v1.
type TranscriptMessage struct {
	Seq        int64
	Contract   string
	Role       string
	Content    string
	ToolCallID string
	ToolName   string
	Arguments  string
}

func (s *Store) ListMessages(ctx context.Context, runID string) ([]TranscriptMessage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT seq, contract, role, content, tool_call_id, tool_name, arguments
		FROM model_messages WHERE run_id=$1 ORDER BY seq ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TranscriptMessage
	for rows.Next() {
		var msg TranscriptMessage
		if err := rows.Scan(&msg.Seq, &msg.Contract, &msg.Role, &msg.Content, &msg.ToolCallID, &msg.ToolName, &msg.Arguments); err != nil {
			return nil, err
		}
		out = append(out, msg)
	}
	return out, rows.Err()
}

func (s *Store) AppendMessage(ctx context.Context, runID string, msg TranscriptMessage) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := lockRun(ctx, tx, runID); err != nil {
			return err
		}
		return appendTranscript(ctx, tx, runID, msg)
	})
}

func appendTranscript(ctx context.Context, tx pgx.Tx, runID string, msg TranscriptMessage) error {
	switch msg.Role {
	case "system", "user", "assistant", "tool":
	default:
		return errBadRole(msg.Role)
	}
	contract := msg.Contract
	if contract == "" {
		contract = model.TranscriptContract
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) + 1 FROM model_messages WHERE run_id=$1`, runID).Scan(&seq); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO model_messages (run_id, seq, contract, role, content, tool_call_id, tool_name, arguments)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		runID, seq, contract, msg.Role, msg.Content, msg.ToolCallID, msg.ToolName, msg.Arguments)
	return err
}

func errBadRole(role string) error {
	return &roleError{role: role}
}

type roleError struct{ role string }

func (e *roleError) Error() string { return "transcript role " + e.role }
