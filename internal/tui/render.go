package tui

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/csy20/agt/internal/usage"
)

const helpText = "enter send   ctrl+j newline   ctrl+y approve   ctrl+n reject   ctrl+o focus   ctrl+f details   /help"

// Render draws one frame. Color uses Lip Gloss. A plain frame has no ANSI codes.
func Render(s Snapshot) string {
	width := s.Width
	if width < 20 {
		width = 20
	}
	var lines []string
	lines = append(lines, paint(s, fit(header(s), width), "111"))
	if s.Example {
		lines = append(lines, paint(s, fit("Example data. Repository files were not changed.", width), "179"))
	}
	if s.DaemonErr != "" {
		lines = append(lines, paint(s, fit(s.DaemonErr, width), "167"))
	}
	lines = append(lines, strings.Repeat("─", width))
	body := bodyLines(s, width)
	body = window(body, transcriptHeight(s), s.Scroll)
	lines = append(lines, body...)
	if s.View == "diff" && s.Diff != "" {
		lines = append(lines, strings.Repeat("─", width))
		lines = append(lines, paint(s, fit("patch  digest "+displayID(s.Digest, s.ShowIDs), width), "114"))
		lines = append(lines, wrapBlock(s.Diff, width)...)
		if note := FollowUpNote(displayID(s.Digest, s.ShowIDs), false); note != "" {
			lines = append(lines, wrapBlock(note, width)...)
		}
	}
	if s.View == "apply" && s.Digest != "" {
		for _, line := range wrapBlock("Apply this full digest to the recorded repository? "+s.Digest, width) {
			lines = append(lines, paint(s, line, "179"))
		}
		hint := "ctrl+o focuses confirm. Composer text does not apply."
		if s.Focus == "apply" {
			hint = "y apply   n cancel"
		}
		lines = append(lines, fit(hint, width))
	}
	if s.View == "help" || s.help {
		lines = append(lines, wrapBlock(commandHelp(), width)...)
	}
	lines = append(lines, strings.Repeat("─", width))
	if s.LiveInput != "" {
		lines = append(lines, strings.TrimRight(s.LiveInput, "\n"))
	} else {
		prompt := s.Prompt
		if prompt == "" {
			prompt = s.Placeholder
		}
		for _, line := range wrapBlock(prompt, width-2) {
			lines = append(lines, fit("│ "+line, width))
		}
	}
	lines = append(lines, paint(s, fit(status(s), width), "60"))
	help := helpText
	if s.Example {
		help += "   alt+1-6 scenes"
	}
	if width >= 70 && s.View != "help" {
		lines = append(lines, paint(s, fit(help, width), "60"))
	}
	if s.Notice != "" {
		lines = append(lines, wrapBlock(s.Notice, width)...)
	}
	return strings.Join(lines, "\n")
}

func header(s Snapshot) string {
	repo := s.Repo
	if repo == "" {
		repo = "no repository"
	}
	mode := s.Mode
	if mode == "" {
		mode = "agent"
	}
	label := "session"
	if s.Example {
		label = "example"
		if s.Scene != "" {
			label += " " + s.Scene
		}
	}
	return "agt  " + baseName(repo) + "  " + label + "  " + mode
}

func status(s Snapshot) string {
	model := s.Model
	if model == "" {
		model = "model unset"
	}
	if s.ModelConfig != "" {
		model = s.ModelConfig + " · " + model
	}
	if s.Scripted {
		model = "scripted " + model
	}
	state := s.RunState
	if state == "" {
		state = "idle"
	}
	mode := s.Mode
	if mode == "" {
		mode = "agent"
	}
	modeNote := mode
	if mode == "ask" || mode == "plan" {
		modeNote = mode + " read-only"
	}
	profile := s.Profile
	if profile == "" {
		profile = "profile unset"
	}
	network := s.Network
	if network == "" {
		network = "none"
	}
	line := model + " · " + state + " · " + modeNote + " · " + profile +
		" · network " + network + " · spent " + usage.USD(s.SpentMicros) +
		" · estimate $" + itoa(int64(s.BudgetCents)/100) + "." + cents(s.BudgetCents)
	if s.Task != "" {
		line += " · task " + s.Task
	}
	if s.Verification != "" {
		line += " · verification " + s.Verification
	}
	return line
}

func bodyLines(s Snapshot, width int) []string {
	var lines []string
	if s.View == "picker" {
		intro := "Sessions for this repository. Enter opens the selected row and does not start a run."
		if s.PickerKind == "model" {
			intro = "Model configurations. Enter selects one and does not call a model."
		}
		lines = append(lines, wrapBlock(intro, width)...)
		for i, row := range s.Picker {
			cursor := " "
			if i == s.PickerAt {
				cursor = ">"
			}
			lines = append(lines, fit(cursor+" "+row, width))
		}
		if s.PickerKind == "model" {
			lines = append(lines, wrapBlock("Esc leaves this list. No inference request is made.", width)...)
		} else {
			lines = append(lines, wrapBlock("Opening does not replay tools or apply a patch.", width)...)
		}
		return lines
	}
	for _, pending := range s.Pending {
		label := "pending " + pending.Change + " " + displayID(pending.RunID, s.ShowIDs)
		lines = append(lines, paint(s, fit(label, width), "179"))
		if pending.Digest != "" {
			lines = append(lines, wrapBlock("digest "+displayID(pending.Digest, s.ShowIDs), width)...)
		}
		note := pending.Note
		if note == "" {
			note = "This patch is still pending. Its edits are absent from the new snapshot. It was not applied and it was not discarded."
		}
		lines = append(lines, wrapBlock(note, width)...)
	}
	if len(s.Messages) == 0 && s.Approval == nil && s.Diff == "" && len(s.Pending) == 0 {
		lines = append(lines, wrapBlock("Repair a failing test. /plan and /ask are read-only. /fix starts a repair run. /feature and /refactor need a contract.", width)...)
	}
	if s.Task != "" || s.Verification != "" || len(s.Checks) > 0 || len(s.Failures) > 0 {
		label := "task " + emptyAs(s.Task, "repair")
		if s.Verification != "" {
			label += " · verification " + s.Verification
		}
		lines = append(lines, paint(s, fit(label, width), "111"))
		for _, check := range s.Checks {
			lines = append(lines, wrapBlock("check "+check, width)...)
		}
		for _, failure := range s.Failures {
			lines = append(lines, wrapBlock("failure "+failure, width)...)
		}
	}
	for _, msg := range s.Messages {
		role := msg.Role
		if role == "" {
			role = "agt"
		}
		lines = append(lines, paint(s, role, "111"))
		lines = append(lines, wrapBlock(msg.Text, width)...)
		lines = append(lines, "")
	}
	for i, tool := range s.Tools {
		mark := "▸"
		if tool.Expanded {
			mark = "▾"
		}
		cursor := " "
		if i == s.Selected {
			cursor = ">"
		}
		label := tool.Name
		if tool.Detail != "" {
			label += "  " + tool.Detail
		}
		if tool.Exit != "" {
			label += "  exit " + tool.Exit
		}
		lines = append(lines, fit(cursor+mark+" "+label, width))
		if tool.Expanded && tool.Output != "" {
			for _, line := range wrapBlock(tool.Output, width-2) {
				lines = append(lines, fit("  "+line, width))
			}
		}
	}
	if s.Approval != nil && s.View != "diff" {
		lines = append(lines, "")
		lines = append(lines, paint(s, fit("Approve "+s.Approval.Tool, width), "179"))
		if s.Approval.Path != "" {
			lines = append(lines, fit(s.Approval.Path, width))
		}
		lines = append(lines, wrapBlock(s.Approval.Reason, width)...)
		lines = append(lines, fit(s.Approval.CommandID, width))
		lines = append(lines, wrapBlock("hash "+displayID(s.Approval.ArgsHash, s.ShowIDs), width)...)
		hint := "ctrl+y approve   ctrl+n reject   ctrl+o focus"
		if s.Focus == "approval" {
			hint = "y approve   n reject   d diff"
		}
		lines = append(lines, fit(hint, width))
		lines = append(lines, wrapBlock("The decision is sent with this hash. A different hash is refused. Composer text does not approve.", width)...)
	}
	return lines
}

func commandHelp() string {
	return strings.Join([]string{
		"/fix [goal]    repair run, one snapshot",
		"/feature FILE goal   feature run. FILE is a contract this client reads.",
		"/refactor FILE goal  refactor run. The same checks must pass before and after.",
		"/plan [goal]   read-only plan. Writes and tests are denied.",
		"/ask [goal]    read-only questions. Same daemon gate as /plan.",
		"/diff          show the patch preview and digest",
		"/apply         confirm the digest, then write the recorded repository",
		"/model [name]  choose a saved configuration. This does not call a model.",
		"/resume        resume a paused run",
		"/cancel        cancel the run. Leaving this screen does not.",
		"/setup         confirm the profile and test command",
		"/sessions      list saved sessions for this repository",
		"/help          show this list",
		"ctrl+y and ctrl+n decide an approval. y and n work only on the focused confirm line.",
		"Composer text never approves or applies. A session can hold many runs.",
	}, "\n")
}

func transcriptHeight(s Snapshot) int {
	height := s.Height
	if height <= 0 {
		height = 24
	}
	room := height - 8
	if room < 6 {
		room = 6
	}
	return room
}

func window(lines []string, height, scroll int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	end := len(lines) - scroll
	if end < height {
		end = height
	}
	if end > len(lines) {
		end = len(lines)
	}
	start := end - height
	if start < 0 {
		start = 0
	}
	return lines[start:end]
}

func wrapBlock(text string, width int) []string {
	text = sanitize(text)
	if width < 8 {
		width = 8
	}
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		if paragraph == "" {
			out = append(out, "")
			continue
		}
		rest := paragraph
		for rest != "" {
			if lipgloss.Width(rest) <= width {
				out = append(out, rest)
				break
			}
			cut := width
			if cut > len(rest) {
				cut = len(rest)
			}
			space := strings.LastIndex(rest[:cut], " ")
			if space > width/2 {
				cut = space
			}
			for cut > 0 && lipgloss.Width(rest[:cut]) > width {
				cut--
			}
			if cut <= 0 {
				cut = 1
			}
			out = append(out, rest[:cut])
			rest = strings.TrimLeft(rest[cut:], " ")
		}
	}
	return out
}

func fit(text string, width int) string {
	text = sanitize(text)
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text + strings.Repeat(" ", width-lipgloss.Width(text))
	}
	cut := len(text)
	for cut > 0 && lipgloss.Width(text[:cut]) > width-1 {
		cut--
	}
	if cut < 1 {
		return strings.Repeat("…", 1)
	}
	return text[:cut] + "…"
}

func paint(s Snapshot, text, color string) string {
	if !s.Color || text == "" {
		return text
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(text)
}

func baseName(path string) string {
	path = strings.TrimRight(path, "/")
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// sanitize removes terminal control sequences from untrusted text.
// Newlines and tabs stay. The stored approval hash is not passed through here
// before it is sent back to the daemon.
func sanitize(s string) string {
	if s == "" || !strings.ContainsAny(s, "\x1b\x7f") && !hasControl(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
			i += size
			continue
		}
		if r == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func hasControl(s string) bool {
	for _, r := range s {
		if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}

func skipEscape(s string, i int) int {
	if i >= len(s) || s[i] != 0x1b {
		return i + 1
	}
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[':
		j := i + 2
		for j < len(s) {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
			j++
		}
		return len(s)
	case ']':
		j := i + 2
		for j < len(s) {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
			j++
		}
		return len(s)
	default:
		return i + 2
	}
}

func displayID(id string, full bool) string {
	id = sanitize(id)
	if full || len(id) <= 16 {
		return id
	}
	return id[:12] + "…"
}

func cents(budget int) string {
	n := budget % 100
	if n < 0 {
		n = -n
	}
	if n < 10 {
		return "0" + itoa(int64(n))
	}
	return itoa(int64(n))
}
