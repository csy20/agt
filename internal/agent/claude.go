package agent

import (
	"encoding/json"
	"strconv"
	"strings"
)

// parseClaude accepts one stream-json object from `claude -p --output-format stream-json`.
// System subtypes are init, api_retry, plugin_install, and permission_denied.
// A claude_code_version below 2.1.259, or with a major version other than 2, is rejected.
// Versions in 2.1.259 and later are accepted as the documented stream, not as a live run.
func (s *Stream) parseClaude(line []byte) error {
	var ev struct {
		Type              string          `json:"type"`
		Subtype           string          `json:"subtype"`
		Protocol          string          `json:"protocol"`
		SessionID         string          `json:"session_id"`
		ParentToolUseID   *string         `json:"parent_tool_use_id"`
		ClaudeCodeVersion string          `json:"claude_code_version"`
		Message           json.RawMessage `json:"message"`
		Event             json.RawMessage `json:"event"`
		IsError           bool            `json:"is_error"`
		Result            string          `json:"result"`
		PermissionDenials json.RawMessage `json:"permission_denials"`
		TotalCostUSD      json.RawMessage `json:"total_cost_usd"`
		Usage             json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(line, &ev); err != nil {
		return protocolf("claude event is not JSON")
	}
	if err := s.checkProtocol(ev.Protocol); err != nil {
		return err
	}
	s.tr.Events++
	if ev.SessionID != "" {
		if s.tr.ProviderSession != "" && s.tr.ProviderSession != ev.SessionID {
			return protocolf("session id changed")
		}
		s.tr.ProviderSession = ev.SessionID
	}
	switch ev.Type {
	case "system":
		return s.parseClaudeSystem(ev.Subtype, ev.ClaudeCodeVersion)
	case "assistant":
		return s.parseClaudeMessage(ev.Message, ev.ParentToolUseID, false)
	case "user":
		return s.parseClaudeMessage(ev.Message, ev.ParentToolUseID, true)
	case "stream_event":
		return s.parseClaudeStream(ev.Event)
	case "result":
		return s.parseClaudeResult(ev.Subtype, ev.IsError, ev.Result, ev.PermissionDenials, ev.TotalCostUSD, ev.Usage)
	default:
		return protocolf("claude event type %q", ev.Type)
	}
}

func (s *Stream) parseClaudeSystem(subtype, version string) error {
	switch subtype {
	case "init":
		if version != "" && !claudeVersionSupported(version) {
			return protocolf("claude_code_version %q", version)
		}
		return nil
	case "api_retry", "plugin_install":
		return nil
	case "permission_denied":
		s.tr.PermissionDenials++
		return nil
	default:
		return protocolf("claude system subtype %q", subtype)
	}
}

func (s *Stream) parseClaudeMessage(raw json.RawMessage, parent *string, user bool) error {
	if len(raw) == 0 || string(raw) == "null" {
		return protocolf("claude message is missing")
	}
	var msg struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return protocolf("claude message")
	}
	subagent := parent != nil && strings.TrimSpace(*parent) != ""
	for _, block := range msg.Content {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &head); err != nil {
			return protocolf("claude content block")
		}
		switch head.Type {
		case "text":
			if user {
				continue
			}
			var text struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(block, &text); err != nil {
				return protocolf("claude text")
			}
			s.text.WriteString(text.Text)
		case "thinking", "redacted_thinking":
			continue
		case "tool_use":
			if user {
				return protocolf("tool_use in a user message")
			}
			req, err := translateClaudeTool(block, subagent)
			if err != nil {
				return err
			}
			s.noteTool(req)
		case "tool_result":
			if !user {
				return protocolf("tool_result in an assistant message")
			}
		default:
			return protocolf("claude content type %q", head.Type)
		}
	}
	return nil
}

func (s *Stream) parseClaudeStream(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return protocolf("stream_event requires event")
	}
	var ev struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return protocolf("stream_event")
	}
	switch ev.Delta.Type {
	case "text_delta":
		s.text.WriteString(ev.Delta.Text)
		return nil
	case "input_json_delta":
		// Partial tool arguments are not a tool call. They are discarded.
		s.tr.DiscardedPartials++
		return nil
	case "":
		return nil
	default:
		return protocolf("claude delta type %q", ev.Delta.Type)
	}
}

func (s *Stream) parseClaudeResult(subtype string, isError bool, result string, denials, cost, usage json.RawMessage) error {
	switch subtype {
	case "", "success", "error":
	default:
		return protocolf("claude result subtype %q", subtype)
	}
	n, err := denialCount(denials)
	if err != nil {
		return err
	}
	s.tr.PermissionDenials += n
	if err := s.recordClaudeUsage(usage); err != nil {
		return err
	}
	if err := s.recordClaudeCost(cost); err != nil {
		return err
	}
	if s.text.Len() == 0 && result != "" {
		s.text.WriteString(result)
	}
	if isError || subtype == "error" {
		s.tr.Outcome = OutcomeFailed
		if s.tr.Error == "" {
			s.tr.Error = "result is_error"
		}
	} else {
		s.tr.Outcome = OutcomeCompleted
	}
	s.terminal = true
	return nil
}

func denialCount(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return 0, protocolf("permission_denials")
	}
	return len(items), nil
}

func (s *Stream) recordClaudeUsage(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var u struct {
		Input  int `json:"input_tokens"`
		Output int `json:"output_tokens"`
	}
	if err := json.Unmarshal(raw, &u); err != nil {
		return protocolf("claude usage")
	}
	s.tr.Usage = Usage{InputTokens: u.Input, OutputTokens: u.Output, Known: true}
	return nil
}

func (s *Stream) recordClaudeCost(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "null" {
		s.tr.CostKnown = false
		s.tr.CostProvenance = "absent"
		return nil
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return protocolf("total_cost_usd")
	}
	s.tr.CostUSD = strconv.FormatFloat(n, 'f', -1, 64)
	s.tr.CostKnown = true
	s.tr.CostProvenance = "claude client estimate, not a bill and not an AGT dollar cap"
	return nil
}

func claudeVersionSupported(v string) bool {
	major, minor, patch, ok := parseSemver(v)
	return ok && major == 2 && minor == 1 && patch >= 259
}
