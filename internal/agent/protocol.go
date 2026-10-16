// Package agent translates documented Codex and Claude Code event streams
// into AGT policy decisions. Fixture parsers do not start either CLI.
// Ask is the only launch path, and it is Codex app-server, read-only, and
// refused when the sandbox preflight cannot confine reads or writes.
// A parsed fixture is scripted. A login token is never read or printed.
package agent

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

const (
	// KindModelAPI is an OpenAI-compatible configuration. It is not a coding agent.
	KindModelAPI = "model-api"
	// KindCodingAgent is an external CLI that owns its own tools.
	KindCodingAgent = "coding-agent"

	// ProtocolCodex is codex exec --json as documented on 16 October 2026.
	// Accepted event types are thread.started, turn.started, turn.completed,
	// turn.failed, item.started, item.completed, and error.
	ProtocolCodex = "codex-exec-jsonl/1"
	// ProtocolClaude is claude -p --output-format stream-json as documented
	// on 16 October 2026. Accepted types are system, assistant, user, result,
	// and stream_event.
	ProtocolClaude = "claude-stream-json/1"

	// LabelScripted marks a fixture. It is not a live model or agent run.
	LabelScripted = "scripted"

	maxEvent = 1 << 20
)

// Outcome is how a fixture ended. It is not an AGT run state.
type Outcome string

const (
	OutcomeOpen       Outcome = "open"
	OutcomeCompleted  Outcome = "completed"
	OutcomeFailed     Outcome = "failed"
	OutcomeIncomplete Outcome = "incomplete"
	OutcomeCancelled  Outcome = "cancelled"
)

var (
	// ErrProtocolMismatch means the bytes are not the pinned protocol.
	ErrProtocolMismatch = errors.New("protocol mismatch")
	// ErrLiveUnavailable means this process will not start Codex or Claude.
	ErrLiveUnavailable = errors.New("live coding-agent execution is unavailable")
	// ErrMutationUnavailable means a writing or command-running adapter is not enabled.
	ErrMutationUnavailable = errors.New("mutating coding-agent execution is unavailable")
	// ErrLogin means Codex is not in a ChatGPT subscription session.
	ErrLogin = errors.New("codex subscription login is unavailable")
	// ErrBoundary means the app-server sandbox cannot confine the ask.
	ErrBoundary = errors.New("codex app-server boundary is not enforceable")
)

// Usage is token accounting copied from a fixture. Zero values are not a dollar cap.
type Usage struct {
	InputTokens           int  `json:"input_tokens"`
	CachedInputTokens     int  `json:"cached_input_tokens,omitempty"`
	OutputTokens          int  `json:"output_tokens"`
	ReasoningOutputTokens int  `json:"reasoning_output_tokens,omitempty"`
	Known                 bool `json:"known"`
}

// ToolRequest is one external tool call after AGT policy. Executed is always false.
type ToolRequest struct {
	CallID       string `json:"call_id,omitempty"`
	ProviderTool string `json:"provider_tool"`
	ProviderArgs []byte `json:"provider_args,omitempty"`
	AGTTool      string `json:"agt_tool,omitempty"`
	AGTArgs      []byte `json:"agt_args,omitempty"`
	ArgsHash     string `json:"args_hash,omitempty"`
	Decision     string `json:"decision"`
	Mediated     bool   `json:"mediated"`
	Executed     bool   `json:"executed"`
	Subagent     bool   `json:"subagent,omitempty"`
	Partial      bool   `json:"partial,omitempty"`
	Reason       string `json:"reason"`
}

// Transcript is a parsed fixture. ProviderSession belongs to the external CLI.
// It is not an AGT session id and not an AGT run id.
type Transcript struct {
	Protocol          string        `json:"protocol"`
	Label             string        `json:"label"`
	ProviderSession   string        `json:"provider_session,omitempty"`
	Text              string        `json:"text,omitempty"`
	Tools             []ToolRequest `json:"tools,omitempty"`
	Usage             Usage         `json:"usage"`
	CostUSD           string        `json:"cost_usd,omitempty"`
	CostKnown         bool          `json:"cost_known"`
	CostProvenance    string        `json:"cost_provenance,omitempty"`
	Outcome           Outcome       `json:"outcome"`
	Error             string        `json:"error,omitempty"`
	PermissionDenials int           `json:"permission_denials"`
	Events            int           `json:"events"`
	DiscardedPartials int           `json:"discarded_partials"`
}

// Stream assembles one protocol from arbitrary chunks. A partial line is not an event.
type Stream struct {
	kind     string
	buf      []byte
	tr       Transcript
	failed   error
	terminal bool
	finished map[string]bool
	pending  map[string]struct{}
	text     strings.Builder
}

// NewStream starts a scripted parser for a pinned protocol.
func NewStream(protocol string) (*Stream, error) {
	switch protocol {
	case ProtocolCodex, ProtocolClaude:
	default:
		return nil, protocolf("unsupported protocol %q", protocol)
	}
	return &Stream{
		kind:     protocol,
		finished: map[string]bool{},
		pending:  map[string]struct{}{},
		tr: Transcript{
			Protocol: protocol,
			Label:    LabelScripted,
			Outcome:  OutcomeOpen,
		},
	}, nil
}

// Push accepts the next bytes. A split JSON object stays buffered until its newline.
func (s *Stream) Push(chunk []byte) error {
	if s.failed != nil {
		return s.failed
	}
	s.buf = append(s.buf, chunk...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			if len(s.buf) > maxEvent {
				return s.fail(protocolf("event exceeds %d bytes", maxEvent))
			}
			return nil
		}
		line := s.buf[:i]
		s.buf = append([]byte(nil), s.buf[i+1:]...)
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > maxEvent {
			return s.fail(protocolf("event exceeds %d bytes", maxEvent))
		}
		if err := s.dispatch(line); err != nil {
			return s.fail(err)
		}
	}
}

// Close finishes the fixture. Exit 143 with no terminal event is cancellation.
// A non-zero exit without a terminal event is a failure. Exit 0 without one is incomplete.
// Leftover bytes that are not a full line are a protocol mismatch and are not executed.
func (s *Stream) Close(exitCode int, cancelled bool) error {
	var proto error
	if strings.TrimSpace(string(s.buf)) != "" {
		proto = protocolf("truncated event")
		s.buf = nil
	}
	if exitCode == 143 {
		cancelled = true
	}
	if s.tr.Outcome == OutcomeOpen {
		switch {
		case cancelled:
			s.tr.Outcome = OutcomeCancelled
			if s.tr.Error == "" {
				s.tr.Error = "process cancelled before a terminal event"
			}
		case exitCode != 0:
			s.tr.Outcome = OutcomeFailed
			s.tr.Error = fmt.Sprintf("process exit %d", exitCode)
		default:
			s.tr.Outcome = OutcomeIncomplete
			if s.tr.Error == "" {
				s.tr.Error = "stream ended without a terminal event"
			}
		}
	}
	s.finishPending()
	s.tr.Text = s.text.String()
	s.tr.Label = LabelScripted
	if proto != nil && s.failed == nil {
		s.failed = proto
	}
	return s.failed
}

// Transcript returns the fixture parsed so far. Tools have Executed false.
func (s *Stream) Transcript() Transcript {
	out := s.tr
	out.Text = s.text.String()
	out.Label = LabelScripted
	if out.Tools == nil {
		out.Tools = []ToolRequest{}
	}
	return out
}

func (s *Stream) dispatch(line []byte) error {
	if s.terminal {
		return protocolf("event after a terminal event")
	}
	switch s.kind {
	case ProtocolCodex:
		return s.parseCodex(line)
	case ProtocolClaude:
		return s.parseClaude(line)
	default:
		return protocolf("unsupported protocol %q", s.kind)
	}
}

func (s *Stream) fail(err error) error {
	s.failed = err
	s.buf = nil
	if s.tr.Outcome == OutcomeOpen {
		s.tr.Outcome = OutcomeFailed
	}
	if s.tr.Error == "" {
		s.tr.Error = err.Error()
	}
	return err
}

func (s *Stream) noteTool(req ToolRequest) {
	req.Executed = false
	s.tr.Tools = append(s.tr.Tools, req)
}

func (s *Stream) finishPending() {
	for id := range s.pending {
		s.noteTool(ToolRequest{
			CallID:       id,
			ProviderTool: "command_execution",
			Decision:     "deny",
			Mediated:     false,
			Executed:     false,
			Partial:      true,
			Reason:       "command_execution ended before item.completed and was not executed",
		})
		delete(s.pending, id)
	}
}

func (s *Stream) checkProtocol(got string) error {
	if got == "" || got == s.kind {
		return nil
	}
	return protocolf("protocol %q", got)
}

func protocolf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrProtocolMismatch}, args...)...)
}
