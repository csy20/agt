package agent

import (
	"encoding/json"
	"strings"
)

// parseCodex accepts one JSONL object from `codex exec --json`.
// Item types other than agent_message and command_execution are rejected.
// The fetched non-interactive sample did not give field schemas for reasoning,
// file changes, MCP calls, web search, or plan updates, so those lines are a
// protocol mismatch and are not executed.
func (s *Stream) parseCodex(line []byte) error {
	var ev struct {
		Type     string          `json:"type"`
		Protocol string          `json:"protocol"`
		ThreadID string          `json:"thread_id"`
		Message  string          `json:"message"`
		Item     json.RawMessage `json:"item"`
		Usage    json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return protocolf("codex event is not JSON")
	}
	if err := s.checkProtocol(ev.Protocol); err != nil {
		return err
	}
	s.tr.Events++
	switch ev.Type {
	case "thread.started":
		if strings.TrimSpace(ev.ThreadID) == "" {
			return protocolf("thread.started requires thread_id")
		}
		if s.tr.ProviderSession != "" && s.tr.ProviderSession != ev.ThreadID {
			return protocolf("thread id changed")
		}
		s.tr.ProviderSession = ev.ThreadID
		return nil
	case "turn.started":
		if s.tr.Outcome == OutcomeCompleted {
			s.tr.Outcome = OutcomeOpen
		}
		return nil
	case "turn.completed":
		if err := s.recordCodexUsage(ev.Usage); err != nil {
			return err
		}
		s.tr.Outcome = OutcomeCompleted
		s.tr.CostKnown = false
		s.tr.CostProvenance = "codex turn.completed has token counts only; no dollar field was in the documented sample"
		return nil
	case "turn.failed":
		s.tr.Outcome = OutcomeFailed
		s.tr.Error = strings.TrimSpace(ev.Message)
		if s.tr.Error == "" {
			s.tr.Error = "turn.failed"
		}
		s.terminal = true
		return nil
	case "error":
		s.tr.Outcome = OutcomeFailed
		s.tr.Error = strings.TrimSpace(ev.Message)
		if s.tr.Error == "" {
			s.tr.Error = "error"
		}
		s.terminal = true
		return nil
	case "item.started", "item.completed":
		return s.parseCodexItem(ev.Type, ev.Item)
	default:
		return protocolf("codex event type %q", ev.Type)
	}
}

func (s *Stream) recordCodexUsage(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		s.tr.Usage.Known = false
		return nil
	}
	var u struct {
		Input     int `json:"input_tokens"`
		Cached    int `json:"cached_input_tokens"`
		Output    int `json:"output_tokens"`
		Reasoning int `json:"reasoning_output_tokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return protocolf("codex usage")
	}
	s.tr.Usage = Usage{
		InputTokens:           u.Input,
		CachedInputTokens:     u.Cached,
		OutputTokens:          u.Output,
		ReasoningOutputTokens: u.Reasoning,
		Known:                 true,
	}
	return nil
}

func (s *Stream) parseCodexItem(event string, raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return protocolf("%s requires item", event)
	}
	var item struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Text    string `json:"text"`
		Command string `json:"command"`
		Status  string `json:"status"`
	}
	if err := json.Unmarshal(raw, &item); err != nil {
		return protocolf("codex item")
	}
	switch item.Type {
	case "agent_message", "command_execution":
	default:
		return protocolf("codex item type %q", item.Type)
	}
	if item.ID == "" {
		return protocolf("codex item requires id")
	}
	if event == "item.started" {
		if item.Type == "command_execution" {
			s.pending[item.ID] = struct{}{}
		}
		return nil
	}
	if s.finished[item.ID] {
		return protocolf("duplicate item %s", item.ID)
	}
	s.finished[item.ID] = true
	delete(s.pending, item.ID)
	if item.Type == "agent_message" {
		if item.Text != "" {
			if s.text.Len() > 0 {
				s.text.WriteByte('\n')
			}
			s.text.WriteString(item.Text)
		}
		return nil
	}
	s.noteTool(denyUnmediated(item.ID, "command_execution", append([]byte(nil), raw...),
		"codex command_execution is a host command and is not an AGT tool"))
	return nil
}
