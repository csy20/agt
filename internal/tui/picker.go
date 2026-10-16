package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/csy20/agt/internal/client"
	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/profile"
)

type pickerItem struct {
	ID    string
	Label string
	New   bool
}

type sessionListMsg struct {
	sessions []client.Session
	err      error
}

type sessionViewMsg struct {
	view client.SessionView
	err  error
}

type submissionMsg struct {
	sub       client.Submission
	err       error
	text      string
	mode      string
	sessionID string
}

func (m model) loadSessions() tea.Cmd {
	if m.client == nil || m.opts.Demo {
		return nil
	}
	if m.opts.Session != "" {
		return m.openSession(m.opts.Session)
	}
	return m.loadSessionList()
}

func (m model) loadSessionList() tea.Cmd {
	if m.client == nil || m.opts.Repo == "" {
		return nil
	}
	c := m.client
	repo := m.opts.Repo
	return func() tea.Msg {
		sessions, err := c.ListSessions(context.Background(), repo)
		return sessionListMsg{sessions: sessions, err: err}
	}
}

func (m model) openSession(sessionID string) tea.Cmd {
	if m.client == nil {
		return nil
	}
	c := m.client
	repo := m.opts.Repo
	return func() tea.Msg {
		view, err := c.OpenSession(context.Background(), sessionID, repo)
		return sessionViewMsg{view: view, err: err}
	}
}

type modelListMsg struct {
	configs []client.ModelInfo
	err     error
}

type modelSelectedMsg struct {
	name string
	info client.ModelInfo
	err  error
}

func (m model) loadModels() tea.Cmd {
	if m.client == nil || m.opts.Demo {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		configs, err := c.ListModels(context.Background())
		return modelListMsg{configs: configs, err: err}
	}
}

func (m model) selectModelName(name string) tea.Cmd {
	if m.client == nil || m.opts.Demo {
		return nil
	}
	c := m.client
	sessionID := m.sessionID
	repo := m.opts.Repo
	return func() tea.Msg {
		var info client.ModelInfo
		var err error
		if sessionID != "" {
			info, err = c.SelectSessionModel(context.Background(), sessionID, name, repo)
		} else {
			info, err = c.SelectModel(context.Background(), name)
		}
		return modelSelectedMsg{name: name, info: info, err: err}
	}
}

func (m model) showModels(configs []client.ModelInfo) (model, tea.Cmd) {
	if len(configs) == 0 {
		m.snap.View = "session"
		m.pickerKind = ""
		m.snap.PickerKind = ""
		m.snap.Notice = "No model configurations are available. No inference request was made."
		return m, nil
	}
	m.models = append([]client.ModelInfo(nil), configs...)
	m.picker = nil
	m.snap.Picker = nil
	for _, cfg := range configs {
		kind := cfg.Adapter
		if cfg.Scripted {
			kind = "scripted"
		}
		label := cfg.Name + "  " + kind
		if cfg.Model != "" {
			label += "  " + cfg.Model
		}
		if !cfg.Valid {
			label += "  invalid"
		}
		if cfg.Selected {
			label += "  selected"
		}
		m.picker = append(m.picker, pickerItem{ID: cfg.Name, Label: label})
		m.snap.Picker = append(m.snap.Picker, label)
	}
	m.snap.PickerAt = 0
	m.pickerKind = "model"
	m.snap.PickerKind = "model"
	m.snap.View = "picker"
	m.focus = focusInput
	return m, nil
}

func (m model) acceptModel(msg modelSelectedMsg) (tea.Model, tea.Cmd) {
	m.snap.View = "session"
	m.pickerKind = ""
	m.snap.PickerKind = ""
	if msg.err != nil {
		m.errText = msg.err.Error()
		return m, nil
	}
	name := msg.info.Name
	if name == "" {
		name = msg.name
	}
	m.modelConfig = name
	m.modelAdapter = msg.info.Adapter
	if msg.info.Adapter != "" {
		m.opts.Provider = msg.info.Adapter
	}
	m.snap.ModelConfig = name
	m.snap.Scripted = msg.info.Scripted
	if msg.info.Model != "" {
		m.snap.Model = msg.info.Model
	}
	m.errText = ""
	notice := "Selected " + name
	if msg.info.Model != "" {
		notice += " (" + msg.info.Model + ")"
	}
	m.snap.Notice = notice + ". No inference request was made."
	return m, nil
}

func (m model) showSessionList(sessions []client.Session) (model, tea.Cmd) {
	if len(sessions) == 0 {
		m.snap.View = "session"
		m.snap.Notice = "No saved sessions for this repository. The next prompt starts a new one."
		return m, nil
	}
	m.pickerKind = "session"
	m.snap.PickerKind = "session"
	m.picker = nil
	m.snap.Picker = nil
	for _, sess := range sessions {
		title := sess.Title
		if title == "" {
			title = "(untitled)"
		}
		m.picker = append(m.picker, pickerItem{ID: sess.ID, Label: sess.ID + "  " + title})
		m.snap.Picker = append(m.snap.Picker, sess.ID+"  "+title)
	}
	m.picker = append(m.picker, pickerItem{Label: "new session", New: true})
	m.snap.Picker = append(m.snap.Picker, "new session")
	m.snap.PickerAt = 0
	m.snap.View = "picker"
	m.focus = focusInput
	return m, nil
}

func (m model) openPicker() (tea.Model, tea.Cmd) {
	if m.snap.PickerAt < 0 || m.snap.PickerAt >= len(m.picker) {
		return m, nil
	}
	if m.pickerKind == "model" {
		item := m.models[m.snap.PickerAt]
		m.snap.View = "session"
		m.pickerKind = ""
		m.snap.PickerKind = ""
		if m.opts.Demo {
			m.snap.Notice = "Example session. No inference request was made."
			return m, nil
		}
		return m, m.selectModelName(item.Name)
	}
	item := m.picker[m.snap.PickerAt]
	if item.New {
		m.sessionID = ""
		m.snap.View = "session"
		m.snap.RunID = ""
		m.snap.Messages = nil
		m.snap.Pending = nil
		m.snap.Notice = "New session. The next prompt starts a run from a fresh snapshot. Nothing was applied."
		return m, nil
	}
	m.snap.Notice = "Opening " + item.ID + ". This does not start a run or apply a patch."
	return m, m.openSession(item.ID)
}

func (m model) showSession(view client.SessionView) (model, tea.Cmd) {
	m.sessionID = view.Session.ID
	if view.Session.ModelConfig != "" {
		m.modelConfig = view.Session.ModelConfig
		m.snap.ModelConfig = view.Session.ModelConfig
		if view.Session.ModelConfig == m.identity.ModelConfig {
			m.modelAdapter = m.identity.Provider
			m.snap.Scripted = m.identity.Scripted
			if m.identity.Model != "" {
				m.snap.Model = m.identity.Model
			}
		} else {
			m.modelAdapter = ""
		}
	}
	m.snap.View = "session"
	m.snap.Repo = view.Session.RepoPath
	m.snap.Messages = nil
	for _, msg := range view.Messages {
		role := "you"
		if msg.Role != "user" {
			role = "agt"
		}
		m.snap.Messages = append(m.snap.Messages, Message{Role: role, Text: msg.Content})
	}
	m.snap.Pending = pendingLines(view.Pending)
	m.snap.Notice = pendingNotice(view.Pending)
	m.snap.Tools = nil
	m.snap.Approval = nil
	m.snap.Diff = ""
	m.snap.Digest = ""
	m.applied = false
	if view.ActiveRunID != "" {
		m.snap.RunID = view.ActiveRunID
		m.snap.Notice = "Attached to " + view.ActiveRunID + ". This did not start a new run or apply a patch."
		if m.client != nil && !m.opts.Demo {
			m.after = 0
			return m.watch(view.ActiveRunID)
		}
		return m, nil
	}
	m.snap.RunID = ""
	if m.snap.Notice == "" {
		m.snap.Notice = "Session history. No run was started and no patch was applied."
	}
	return m, nil
}

func pendingLines(pending []client.PendingPatch) []PendingLine {
	var out []PendingLine
	for _, patch := range pending {
		note := patch.Note
		if note == "" {
			note = fmt.Sprintf("Patch from %s is still pending (%s). Its edits are absent from the new snapshot. It was not applied and it was not discarded.", patch.RunID, patch.Change)
		}
		out = append(out, PendingLine{RunID: patch.RunID, Digest: patch.Digest, Change: patch.Change, Note: note})
	}
	return out
}

func pendingNotice(pending []client.PendingPatch) string {
	if len(pending) == 0 {
		return ""
	}
	patch := pending[0]
	return fmt.Sprintf("Patch from %s is still pending (%s). Its edits are absent from the new snapshot. It was not applied and it was not discarded.", patch.RunID, emptyAs(patch.Change, "unapplied"))
}

func (m model) submitSession(text, mode, task string, contract json.RawMessage, msgID string) tea.Cmd {
	c := m.client
	repo := m.opts.Repo
	script := m.opts.Script
	provider := m.opts.Provider
	if provider == "" {
		provider = m.identity.Provider
	}
	modelConfig := m.modelConfig
	if modelConfig != "" {
		provider = m.modelAdapter
	}
	sessionID := m.sessionID
	confirmed := *m.opts.Confirmed
	return func() tea.Msg {
		if sessionID == "" {
			sess, err := c.CreateSession(context.Background(), repo, "")
			if err != nil {
				return submissionMsg{err: err, text: text, mode: mode}
			}
			sessionID = sess.ID
		}
		sub, err := c.SubmitMessage(context.Background(), sessionID, client.CreateRequest{
			Goal: text, RepoPath: repo, Provider: provider, ScriptPath: script,
			BudgetCents: 100, Interaction: mode, TaskType: task, Contract: contract,
			ModelConfig: modelConfig,
			Profile:     profile.Request{Name: confirmed.Profile, Argv: confirmed.Argv, Workdir: confirmed.Workdir},
		}, msgID, text)
		return submissionMsg{sub: sub, err: err, text: text, mode: mode, sessionID: sessionID}
	}
}

func (m model) acceptSubmission(msg submissionMsg) (tea.Model, tea.Cmd) {
	if msg.sessionID != "" {
		m.sessionID = msg.sessionID
	}
	var busy *client.BusyError
	var mismatch *client.RepoMismatchError
	if errors.As(msg.err, &busy) {
		m.errText = busy.Error()
		m.input.SetValue(msg.text)
		return m, nil
	}
	if errors.As(msg.err, &mismatch) {
		m.errText = mismatch.Error()
		m.input.SetValue(msg.text)
		return m, nil
	}
	if msg.err != nil {
		m.errText = msg.err.Error()
		m.input.SetValue(msg.text)
		return m, nil
	}
	m.errText = ""
	m.input.SetValue("")
	m.snap.Messages = append(m.snap.Messages, Message{Role: "you", Text: msg.text})
	m.snap.Mode = msg.mode
	m.mode = msg.mode
	m.snap.Tools = nil
	m.snap.Approval = nil
	m.snap.Diff = ""
	m.snap.Digest = ""
	m.applied = false
	m.snap.View = "session"
	m.snap.Pending = pendingLines(msg.sub.Pending)
	if msg.sub.Notice != "" {
		m.snap.Notice = msg.sub.Notice
	} else if note := pendingNotice(msg.sub.Pending); note != "" {
		m.snap.Notice = note
	}
	if !msg.sub.Created {
		m.snap.Notice = "That client message already exists. Attached to the original run. No second run was created."
	}
	next, cmd := m.UpdateCreated(client.Created{RunID: msg.sub.RunID, State: msg.sub.State})
	return next, cmd
}

func newClientMessageID() string {
	return id.New("msg_")
}
