package tui

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/csy20/agt/internal/client"
	"github.com/csy20/agt/internal/setup"
)

// Options selects the session. Demo draws example data and does not call a daemon.
type Options struct {
	Demo        bool
	Dump        bool
	Color       bool
	Base        string
	Repo        string
	Provider    string
	Script      string
	DaemonErr   string
	Confirmed   *setup.File
	SetupPath   string
	HistoryPath string
	History     []string
	SaveHist    func([]string)
	Session     string
}

type model struct {
	opts      Options
	client    *client.Client
	identity  client.Identity
	snap      Snapshot
	input     textarea.Model
	history   []string
	histAt    int
	after     int64
	events    <-chan tea.Msg
	cancel    context.CancelFunc
	mode      string
	applied   bool
	frames    []Frame
	ready     bool
	errText   string
	focus     focus
	gen       int
	sessionID string
	picker    []pickerItem
}

type focus int

const (
	focusInput focus = iota
	focusApproval
	focusTools
	focusApply
)

type streamDone struct {
	gen int
	err error
}

type eventMsg struct {
	gen int
	ev  client.Event
}
type fetchedCommand struct {
	cmd client.Command
	err error
}
type fetchedDiff struct {
	diff client.Diff
	err  error
}
type actionDone struct {
	label string
	run   client.Run
	err   error
}

func newModel(opts Options) model {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = ""
	ta.Placeholder = "Repair a failing test, or /help"
	ta.CharLimit = 0
	ta.SetWidth(72)
	ta.SetHeight(3)
	ta.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j", "alt+enter"))
	ta.Focus()
	m := model{
		opts: opts, input: ta, history: append([]string(nil), opts.History...),
		histAt: len(opts.History), mode: "agent",
		snap: Snapshot{
			Width: 80, Height: 24, Color: opts.Color, Example: opts.Demo,
			Repo: opts.Repo, Mode: "agent", Profile: profileOf(opts.Confirmed),
			Network: "none", BudgetCents: 100, Placeholder: ta.Placeholder,
			DaemonErr: opts.DaemonErr,
		},
	}
	if opts.Confirmed != nil {
		m.snap.Profile = opts.Confirmed.Profile
	}
	if opts.Demo {
		m.frames = ExampleFrames(96, opts.Color)
		m.snap = m.frames[2].Snapshot
		m.snap.Color = opts.Color
	}
	return m
}

func (m model) Init() tea.Cmd {
	blink := m.input.Focus()
	if m.client == nil {
		return blink
	}
	return tea.Batch(blink, m.loadIdentity(), m.loadSessions())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.snap.Width = msg.Width
		m.snap.Height = msg.Height
		m.input.SetWidth(msg.Width - 2)
		if m.input.Width() < 20 {
			m.input.SetWidth(20)
		}
		m.ready = true
		return m, nil
	case client.Created:
		next, cmd := m.UpdateCreated(msg)
		return next, cmd
	case client.Identity:
		m.identity = msg
		if m.identity.Model != "" {
			m.snap.Model = m.identity.Model
		}
		if m.opts.Provider == "" {
			m.opts.Provider = m.identity.Provider
		}
		return m, nil
	case sessionListMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		next, cmd := m.showSessionList(msg.sessions)
		return next, cmd
	case sessionViewMsg:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		next, cmd := m.showSession(msg.view)
		return next, cmd
	case submissionMsg:
		return m.acceptSubmission(msg)
	case eventMsg:
		if msg.gen != m.gen {
			return m, nil
		}
		if msg.ev.Seq > m.after {
			m.after = msg.ev.Seq
		}
		m.snap = Apply(m.snap, msg.ev)
		m.snap.Mode = m.mode
		var extra tea.Cmd
		if msg.ev.Type == "approval.requested" && m.snap.Approval != nil {
			extra = m.loadCommand(m.snap.Approval.CommandID)
		}
		if msg.ev.Type == "run.succeeded" {
			extra = m.loadDiff()
		}
		return m, tea.Batch(readNext(m.events), extra)
	case streamDone:
		if msg.gen != m.gen {
			return m, nil
		}
		if msg.err != nil && msg.err != io.EOF && msg.err != context.Canceled {
			m.errText = msg.err.Error()
		}
		return m, nil
	case fetchedCommand:
		if msg.err == nil && m.snap.Approval != nil {
			m.snap.Approval.ArgsHash = msg.cmd.ArgsHash
			m.snap.Approval.Tool = msg.cmd.Tool
			m.snap.Approval.Path = pathOf(msg.cmd.Canonical)
		}
		return m, nil
	case fetchedDiff:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.snap.Diff = msg.diff.Diff
		m.snap.Digest = msg.diff.Digest
		m.snap.View = "diff"
		if !msg.diff.Complete && msg.diff.Note != "" {
			m.snap.Notice = msg.diff.Note
		}
		return m, nil
	case actionDone:
		if msg.err != nil {
			m.errText = msg.err.Error()
			return m, nil
		}
		m.errText = ""
		if msg.run.State != "" {
			m.snap.RunState = msg.run.State
		}
		if msg.label == "approved" || msg.label == "rejected" {
			m.snap.Approval = nil
		}
		if msg.label == "applied" {
			m.applied = true
			m.focus = focusInput
			m.snap.Notice = "Applied to the recorded repository. This was not a git commit."
			m.snap.View = "session"
		}
		if (msg.label == "approved" || msg.label == "resume") && m.client != nil && m.snap.RunID != "" && !m.opts.Demo {
			next, cmd := m.watch(m.snap.RunID)
			return next, cmd
		}
		return m, nil
	case tea.KeyMsg:
		if msg.Paste {
			if m.focus != focusInput {
				return m, nil
			}
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
		return m.key(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.cancel != nil {
			m.cancel()
		}
		if m.opts.SaveHist != nil {
			m.opts.SaveHist(m.history)
		}
		return m, tea.Quit
	case "ctrl+f":
		m.snap.ShowIDs = !m.snap.ShowIDs
		return m, nil
	case "ctrl+o":
		if m.snap.View == "apply" && m.snap.Digest != "" {
			m.focus = focusApply
			return m, nil
		}
		if m.snap.Approval != nil {
			m.focus = focusApproval
		}
		return m, nil
	case "ctrl+t":
		if len(m.snap.Tools) > 0 {
			m.focus = focusTools
		}
		return m, nil
	case "ctrl+y":
		if m.snap.Approval != nil {
			return m.decide("approved")
		}
	case "ctrl+n":
		if m.snap.Approval != nil {
			return m.decide("rejected")
		}
	case "pgup":
		m.snap.Scroll += 8
		return m, nil
	case "pgdown":
		if m.snap.Scroll >= 8 {
			m.snap.Scroll -= 8
		} else {
			m.snap.Scroll = 0
		}
		return m, nil
	case "esc":
		if m.focus != focusInput {
			m.focus = focusInput
			return m, nil
		}
		if m.snap.View == "diff" || m.snap.View == "apply" || m.snap.View == "help" {
			m.snap.View = "session"
			m.snap.help = false
			return m, nil
		}
	}
	if m.snap.View == "picker" {
		switch msg.String() {
		case "up":
			if m.snap.PickerAt > 0 {
				m.snap.PickerAt--
			}
			return m, nil
		case "down":
			if m.snap.PickerAt+1 < len(m.picker) {
				m.snap.PickerAt++
			}
			return m, nil
		case "enter":
			return m.openPicker()
		default:
			return m, nil
		}
	}
	if m.focus == focusTools && msg.String() == "tab" {
		if len(m.snap.Tools) == 0 {
			return m, nil
		}
		m.snap.Selected = (m.snap.Selected + 1) % len(m.snap.Tools)
		i := m.snap.Selected
		m.snap.Tools[i].Expanded = !m.snap.Tools[i].Expanded
		return m, nil
	}
	if frame, ok := demoKey(msg.String()); ok {
		if !m.opts.Demo || m.focus != focusInput || frame >= len(m.frames) {
			return m, nil
		}
		width := m.snap.Width
		m.snap = m.frames[frame].Snapshot
		if frame != 5 && width >= 20 {
			m.snap.Width = width
		}
		m.snap.Color = m.opts.Color
		m.focus = focusInput
		return m, nil
	}
	if m.focus == focusApproval && m.snap.Approval != nil && m.snap.View != "diff" && m.snap.View != "apply" {
		switch msg.String() {
		case "y":
			m.focus = focusInput
			return m.decide("approved")
		case "n":
			m.focus = focusInput
			return m.decide("rejected")
		case "d":
			m.snap.View = "diff"
			if m.snap.Diff == "" && !m.opts.Demo {
				return m, m.loadDiff()
			}
			return m, nil
		default:
			return m, nil
		}
	}
	if m.snap.View == "apply" && m.focus == focusApply {
		switch msg.String() {
		case "y":
			return m.applyDigest()
		case "n":
			m.focus = focusInput
			m.snap.View = "diff"
			return m, nil
		default:
			return m, nil
		}
	}
	if m.focus != focusInput {
		return m, nil
	}
	if msg.String() == "enter" {
		return m.submit(m.input.Value())
	}
	if msg.String() == "up" && m.input.Value() == "" {
		m.recall(-1)
		return m, nil
	}
	if msg.String() == "down" && m.input.Line() == 0 {
		m.recall(1)
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func demoKey(s string) (int, bool) {
	switch s {
	case "alt+1":
		return 0, true
	case "alt+2":
		return 1, true
	case "alt+3":
		return 2, true
	case "alt+4":
		return 3, true
	case "alt+5":
		return 4, true
	case "alt+6":
		return 5, true
	default:
		return 0, false
	}
}

func (m model) submit(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if text == "" {
		return m, nil
	}
	m.errText = ""
	m.remember(text)
	m.input.SetValue("")
	if name, rest, ok := ParseSlash(text); ok {
		return m.slash(name, rest)
	}
	if m.opts.Demo {
		m.snap.Notice = "Example session. That prompt was not sent. A follow-up would start a fresh snapshot."
		return m, nil
	}
	return m.startRun(text, m.mode)
}

func (m model) slash(name, rest string) (tea.Model, tea.Cmd) {
	switch name {
	case "help":
		m.snap.View = "help"
		m.snap.help = true
		return m, nil
	case "plan", "ask":
		m.mode = name
		m.snap.Mode = name
		m.snap.Notice = "/plan and /ask are read-only. The daemon denies writes and test runs. PLANNING is still the repair state, not this mode."
		if rest == "" {
			return m, nil
		}
		if m.opts.Demo {
			m.snap.Notice = "Example session. " + m.snap.Notice
			return m, nil
		}
		return m.startRun(rest, name)
	case "fix":
		m.mode = "agent"
		m.snap.Mode = "agent"
		if rest == "" {
			m.snap.Notice = "Mode is agent. The next prompt starts a repair run from a fresh snapshot."
			return m, nil
		}
		if m.opts.Demo {
			m.snap.Notice = "Example session. /fix was not sent."
			return m, nil
		}
		return m.startRun(rest, "agent")
	case "diff":
		m.snap.View = "diff"
		if m.opts.Demo {
			if m.snap.Diff == "" {
				width := m.snap.Width
				m.snap = m.frames[3].Snapshot
				if width >= 20 {
					m.snap.Width = width
				}
				m.snap.Color = m.opts.Color
			}
			return m, nil
		}
		if m.snap.RunID == "" && len(m.snap.Pending) > 0 {
			m.snap.Notice = "Showing the pending patch. Its edits are absent from the new snapshot. This does not apply it."
			return m, m.loadDiffRun(m.snap.Pending[0].RunID)
		}
		return m, m.loadDiff()
	case "apply":
		if m.snap.Digest == "" {
			m.errText = "There is no digest yet. /diff loads the artifact."
			return m, nil
		}
		m.snap.View = "apply"
		m.focus = focusApply
		m.snap.Notice = "The confirm line is focused. y posts the full digest. n leaves the repository unchanged. Composer text does not apply."
		return m, nil
	case "model":
		current := m.snap.Model
		if current == "" {
			current = m.identity.Model
		}
		if rest == "" || rest == current {
			m.snap.Notice = "agtd model is " + emptyAs(current, "unset") + ". --model checks that name. It does not switch models. Set AGT_MODEL_NAME and restart agtd."
			return m, nil
		}
		m.errText = "agtd model is " + emptyAs(current, "unset") + " and /model is " + rest + ". This session does not switch models. Set AGT_MODEL_NAME and restart agtd."
		return m, nil
	case "resume":
		return m.lifecycle("resume")
	case "cancel":
		return m.lifecycle("cancel")
	case "setup":
		return m.confirmSetup(rest)
	case "sessions":
		if m.opts.Demo {
			m.snap.Notice = "Example session. Saved sessions are not listed."
			return m, nil
		}
		m.snap.View = "picker"
		return m, m.loadSessionList()
	default:
		m.errText = "Unknown command /" + name + ". /help lists the commands."
		return m, nil
	}
}

func (m model) confirmSetup(rest string) (tea.Model, tea.Cmd) {
	if rest == "" {
		m.snap.Notice = "Confirm a test command, for example: python -m unittest tests.test_retry. Images stay on the daemon. This does not start a run."
		m.snap.View = "help"
		return m, nil
	}
	suggested := ""
	if m.opts.Confirmed != nil {
		suggested = m.opts.Confirmed.Profile
	}
	file, err := setup.ParseLine(rest, suggested)
	if err != nil {
		m.errText = err.Error()
		return m, nil
	}
	file.Repo = m.opts.Repo
	if m.opts.SetupPath != "" {
		if err := setup.Save(m.opts.SetupPath, file); err != nil {
			m.errText = err.Error()
			return m, nil
		}
	}
	m.opts.Confirmed = &file
	m.snap.Profile = file.Profile
	m.snap.Notice = "Saved the profile and test command. No run was started. The daemon still chooses the image."
	return m, nil
}

func (m model) startRun(text, mode string) (tea.Model, tea.Cmd) {
	if m.client == nil {
		m.errText = emptyAs(m.opts.DaemonErr, "agtd is not connected. Run agt doctor.")
		return m, nil
	}
	if m.opts.Confirmed == nil {
		m.errText = "Confirm a profile and test command with /setup or agt setup. Passing a repository does not select either."
		return m, nil
	}
	if note := FollowUpNote(m.snap.Digest, m.applied); note != "" {
		m.snap.Notice = note
	}
	goal, err := AttachRefs(m.opts.Repo, text)
	if err != nil {
		m.errText = err.Error()
		return m, nil
	}
	provider := m.opts.Provider
	if provider == "" {
		provider = m.identity.Provider
	}
	if provider == "fake" && m.opts.Script == "" {
		m.errText = "agtd provider is fake. Pass --script to agt tui, or start agtd with the openai provider. This session does not invent a script."
		return m, nil
	}
	m.snap.Mode = mode
	m.mode = mode
	return m, m.submitSession(goal, mode, newClientMessageID())
}

func (m model) UpdateCreated(msg client.Created) (tea.Model, tea.Cmd) {
	m.errText = ""
	m.snap.RunID = msg.RunID
	m.snap.RunState = msg.State
	m.after = 0
	next, cmd := m.watch(msg.RunID)
	return next, cmd
}

func (m model) watch(runID string) (model, tea.Cmd) {
	if m.cancel != nil {
		m.cancel()
	}
	m.gen++
	gen := m.gen
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	ch := make(chan tea.Msg, 32)
	c := m.client
	after := m.after
	go func() {
		err := c.ReadEvents(ctx, runID, after, func(ev client.Event) error {
			ch <- eventMsg{gen: gen, ev: ev}
			return nil
		})
		ch <- streamDone{gen: gen, err: err}
	}()
	m.events = ch
	return m, readNext(ch)
}

func (m model) decide(decision string) (tea.Model, tea.Cmd) {
	if m.snap.Approval == nil {
		return m, nil
	}
	hash := m.snap.Approval.ArgsHash
	cmdID := m.snap.Approval.CommandID
	if m.opts.Demo {
		m.errText = ""
		m.snap.Notice = "Example only. " + decision + " was not sent. Hash " + hash + " would be the body."
		m.snap.Approval = nil
		return m, nil
	}
	if m.client == nil || m.snap.RunID == "" {
		m.errText = "There is no run to approve."
		return m, nil
	}
	c := m.client
	runID := m.snap.RunID
	return m, func() tea.Msg {
		view, err := c.Approve(context.Background(), runID, cmdID, hash, decision)
		return actionDone{label: decision, run: view, err: err}
	}
}

func (m model) applyDigest() (tea.Model, tea.Cmd) {
	digest := m.snap.Digest
	if m.opts.Demo {
		m.snap.Notice = "Example only. Nothing was written. A real apply posts digest " + digest + "."
		m.applied = true
		m.focus = focusInput
		m.snap.View = "session"
		return m, nil
	}
	if m.client == nil || m.snap.RunID == "" {
		m.errText = "There is no run to apply."
		return m, nil
	}
	c := m.client
	runID := m.snap.RunID
	return m, func() tea.Msg {
		err := c.Apply(context.Background(), runID, digest)
		return actionDone{label: "applied", err: err}
	}
}

func (m model) lifecycle(action string) (tea.Model, tea.Cmd) {
	if m.opts.Demo {
		m.snap.Notice = "Example only. /" + action + " was not sent. Leaving the screen is not cancel."
		return m, nil
	}
	if m.client == nil || m.snap.RunID == "" {
		m.errText = "There is no run to " + action + "."
		return m, nil
	}
	c := m.client
	runID := m.snap.RunID
	return m, func() tea.Msg {
		view, err := c.Lifecycle(context.Background(), runID, action, map[string]any{})
		return actionDone{label: action, run: view, err: err}
	}
}

func (m model) loadIdentity() tea.Cmd {
	c := m.client
	return func() tea.Msg {
		id, err := c.Identity(context.Background())
		if err != nil {
			return actionDone{err: err}
		}
		return id
	}
}

func (m model) loadCommand(commandID string) tea.Cmd {
	if m.client == nil || m.snap.RunID == "" {
		return nil
	}
	c := m.client
	runID := m.snap.RunID
	return func() tea.Msg {
		cmd, err := c.Command(context.Background(), runID, commandID)
		return fetchedCommand{cmd: cmd, err: err}
	}
}

func (m model) loadDiff() tea.Cmd {
	if m.snap.RunID == "" && len(m.snap.Pending) > 0 {
		return m.loadDiffRun(m.snap.Pending[0].RunID)
	}
	return m.loadDiffRun(m.snap.RunID)
}

func (m model) loadDiffRun(runID string) tea.Cmd {
	if m.client == nil || runID == "" {
		return nil
	}
	c := m.client
	return func() tea.Msg {
		diff, err := c.Diff(context.Background(), runID)
		return fetchedDiff{diff: diff, err: err}
	}
}

func (m model) View() string {
	frame := m.snap
	frame.Color = m.opts.Color
	frame.Focus = focusName(m.focus)
	frame.LiveInput = m.input.View()
	if m.errText != "" {
		frame.Notice = m.errText
	}
	return Render(frame)
}

func focusName(f focus) string {
	switch f {
	case focusApproval:
		return "approval"
	case focusTools:
		return "tools"
	case focusApply:
		return "apply"
	default:
		return "input"
	}
}

func (m *model) remember(text string) {
	if len(m.history) == 0 || m.history[len(m.history)-1] != text {
		m.history = append(m.history, text)
	}
	if len(m.history) > 50 {
		m.history = m.history[len(m.history)-50:]
	}
	m.histAt = len(m.history)
}

func (m *model) recall(delta int) {
	if len(m.history) == 0 {
		return
	}
	next := m.histAt + delta
	if next < 0 {
		next = 0
	}
	if next >= len(m.history) {
		m.histAt = len(m.history)
		m.input.SetValue("")
		return
	}
	m.histAt = next
	m.input.SetValue(m.history[next])
}

func readNext(ch <-chan tea.Msg) tea.Cmd {
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func profileOf(file *setup.File) string {
	if file == nil {
		return ""
	}
	return file.Profile
}

func emptyAs(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func pathOf(raw json.RawMessage) string {
	var body struct {
		Path string `json:"path"`
	}
	_ = json.Unmarshal(raw, &body)
	return body.Path
}
