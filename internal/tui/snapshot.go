package tui

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/csy20/agt/internal/client"
)

// Message is one turn in the session. A session can outlive any single run.
type Message struct {
	Role string
	Text string
}

// PendingLine is an earlier patch that is still unapplied.
type PendingLine struct {
	RunID  string
	Digest string
	Change string
	Note   string
}

// ToolLine is one policy-gated tool call. It starts collapsed.
type ToolLine struct {
	Name     string
	Detail   string
	Exit     string
	Output   string
	State    string
	Expanded bool
}

// Approval is a decision bound to the daemon's argument hash.
type Approval struct {
	Tool      string
	CommandID string
	ArgsHash  string
	Reason    string
	Path      string
}

// Snapshot is the session the renderer draws.
type Snapshot struct {
	Width       int
	Height      int
	Color       bool
	Example     bool
	Repo        string
	Model       string
	Mode        string
	RunID       string
	RunState    string
	Profile     string
	Network     string
	SpentMicros int64
	BudgetCents int
	Messages    []Message
	Tools       []ToolLine
	Selected    int
	Approval    *Approval
	Diff        string
	Digest      string
	View        string
	Prompt      string
	Placeholder string
	LiveInput   string
	Notice      string
	DaemonErr   string
	Scroll      int
	Scene       string
	ShowIDs     bool
	Focus       string
	LastSeq     int64
	Pending     []PendingLine
	Picker      []string
	PickerAt    int
	openAssist  bool
	help        bool
}

// Apply folds one daemon event into the session.
// assistant.delta only adds text. A tool is shown from the command events,
// which the daemon writes after the call is complete.
func Apply(s Snapshot, ev client.Event) Snapshot {
	if ev.Seq > 0 && ev.Seq <= s.LastSeq {
		return s
	}
	if ev.Seq > s.LastSeq {
		s.LastSeq = ev.Seq
	}
	var payload map[string]any
	_ = json.Unmarshal(ev.Data, &payload)
	switch ev.Type {
	case "assistant.delta":
		s.noteAssistant(str(payload["text"]), true)
	case "model.called":
		if name := str(payload["model_name"]); name != "" {
			s.Model = name
		}
		if n, ok := num(payload["cost_micros"]); ok {
			s.SpentMicros += n
		}
		s.noteAssistant(str(payload["text"]), false)
	case "command.started":
		s.Tools = append(s.Tools, ToolLine{Name: str(payload["tool"]), State: "started", Detail: "running"})
	case "command.finished":
		s.finishTool(str(payload["tool"]), str(payload["exit_code"]), str(payload["output_excerpt"]))
	case "approval.requested":
		s.RunState = "AWAITING_APPROVAL"
		s.Approval = &Approval{
			Tool: str(payload["tool"]), CommandID: str(payload["command_id"]),
			ArgsHash: str(payload["args_hash"]), Reason: str(payload["reason"]),
			Path: str(payload["path"]),
		}
		s.Tools = append(s.Tools, ToolLine{
			Name: str(payload["tool"]), State: "approval", Detail: "awaiting approval",
		})
	case "approval.recorded":
		if str(payload["decision"]) == "approved" || str(payload["decision"]) == "rejected" {
			s.Approval = nil
		}
	case "state.changed":
		if to := str(payload["to"]); to != "" {
			s.RunState = to
		}
	case "run.succeeded":
		s.RunState = "SUCCEEDED"
		s.Approval = nil
	case "run.failed":
		s.RunState = "FAILED_TERMINAL"
		s.Notice = str(payload["reason"])
	case "budget.paused":
		s.RunState = "PAUSED_BUDGET"
		s.Notice = "Paused because the reserved cost would exceed the estimate. This is not a hard dollar cap when prices are zero."
	}
	return s
}

func (s *Snapshot) noteAssistant(text string, delta bool) {
	if text == "" && delta {
		return
	}
	if delta {
		if !s.openAssist {
			s.Messages = append(s.Messages, Message{Role: "agt", Text: text})
			s.openAssist = true
			return
		}
		s.Messages[len(s.Messages)-1].Text += text
		return
	}
	if text == "" {
		s.openAssist = false
		return
	}
	if s.openAssist && len(s.Messages) > 0 && s.Messages[len(s.Messages)-1].Role == "agt" {
		s.Messages[len(s.Messages)-1].Text = text
	} else {
		s.Messages = append(s.Messages, Message{Role: "agt", Text: text})
	}
	s.openAssist = false
}

func (s *Snapshot) finishTool(name, exit, output string) {
	for i := len(s.Tools) - 1; i >= 0; i-- {
		if s.Tools[i].Name == name && (s.Tools[i].State == "started" || s.Tools[i].State == "approval") {
			s.Tools[i].State = "finished"
			s.Tools[i].Exit = exit
			s.Tools[i].Output = output
			s.Tools[i].Detail = ""
			return
		}
	}
	s.Tools = append(s.Tools, ToolLine{Name: name, State: "finished", Exit: exit, Output: output})
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case nil:
		return ""
	default:
		return strings.TrimSpace(strings.ReplaceAll(
			strings.TrimPrefix(strings.TrimPrefix(jsonNumber(v), " "), "\n"), "\n", " "))
	}
}

func jsonNumber(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return strings.Trim(string(b), `"`)
}

func num(v any) (int64, bool) {
	switch t := v.(type) {
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case json.Number:
		n, err := t.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}
