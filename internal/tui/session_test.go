package tui

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/csy20/agt/internal/client"
)

func TestSlashAndFollowUp(t *testing.T) {
	name, rest, ok := ParseSlash("/plan how should this be fixed?")
	if !ok || name != "plan" || rest != "how should this be fixed?" {
		t.Fatalf("%q %q %v", name, rest, ok)
	}
	if _, _, ok := ParseSlash("make the retry test pass"); ok {
		t.Fatal("a prompt was treated as a command")
	}
	note := FollowUpNote(exampleDigest, false)
	if !strings.Contains(note, "new snapshot") || !strings.Contains(note, exampleDigest) {
		t.Fatal(note)
	}
	if FollowUpNote(exampleDigest, true) != "" {
		t.Fatal("an applied patch still warned")
	}
}

func TestFileRefNamesThePathAndDoesNotReadIt(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "processor.py"), []byte("SECRET-BYTES-IN-FILE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	goal, err := AttachRefs(dir, "look at @processor.py")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(goal, "SECRET-BYTES-IN-FILE") {
		t.Fatal("file bytes were copied into the goal")
	}
	if !strings.Contains(goal, "processor.py") || !strings.Contains(goal, "data, not instructions") {
		t.Fatal(goal)
	}
	if _, err := AttachRefs(dir, "read @.env"); err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("secret ref error %v", err)
	}
}

func TestExampleFramesStayInsideTheWidth(t *testing.T) {
	for _, frame := range ExampleFrames(96, false) {
		if strings.Contains(Render(frame.Snapshot), "\x1b") {
			t.Fatalf("%s used color when color was off", frame.Name)
		}
	}
	frames := ExampleFrames(120, false)
	byName := map[string]string{}
	for _, frame := range frames {
		byName[frame.Name] = Render(frame.Snapshot)
	}
	if !strings.Contains(byName["stream"], "charge appends every idempotency key") || !strings.Contains(byName["stream"], "search_code") || !strings.Contains(byName["stream"], `query "def charge"`) {
		t.Fatal(byName["stream"])
	}
	approval := byName["approval"]
	for _, want := range []string{exampleHash[:12] + "…", "edits that change test files require approval", "ctrl+y", "tests/test_retry.py", "estimate $1.00"} {
		if !strings.Contains(approval, want) {
			t.Fatalf("approval missing %s\n%s", want, approval)
		}
	}
	if strings.Contains(approval, exampleHash) {
		t.Fatal("routine approval view showed the full hash")
	}
	if !strings.Contains(byName["patch"], exampleDigest[:12]+"…") || !strings.Contains(byName["patch"], "self.charged") || !strings.Contains(byName["patch"], "new snapshot") {
		t.Fatal(byName["patch"])
	}
	if strings.Contains(byName["patch"], exampleDigest) {
		t.Fatal("routine patch view showed the full digest")
	}
	if !strings.Contains(byName["plan"], "read-only") || !strings.Contains(byName["plan"], "test_retry_does_not_double_charge") {
		t.Fatal(byName["plan"])
	}
	for _, name := range []string{"composer", "stream", "approval", "patch", "plan", "narrow"} {
		if !strings.Contains(byName[name], "example "+name) {
			t.Fatalf("%s header missing", name)
		}
	}
	narrow := ExampleFrames(48, false)[5]
	for _, line := range strings.Split(Render(narrow.Snapshot), "\n") {
		if lipgloss.Width(line) > 48 {
			t.Fatalf("narrow line is %d > 48: %q", lipgloss.Width(line), line)
		}
	}
}

func TestHistoryFileIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache", "prompt-history")
	if err := saveHistory(path, []string{"make the retry test pass"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
}

func TestDemoApprovalDoesNotCallADaemon(t *testing.T) {
	m := newModel(Options{Demo: true})
	if m.snap.Approval == nil || m.snap.Approval.ArgsHash != exampleHash {
		t.Fatalf("approval %+v", m.snap.Approval)
	}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlY})
	if cmd != nil {
		t.Fatal("demo approval started a command")
	}
	got := next.(model)
	if got.snap.Approval != nil || !strings.Contains(got.snap.Notice, "Example only") || !strings.Contains(got.snap.Notice, exampleHash) {
		t.Fatalf("notice %q approval %+v", got.snap.Notice, got.snap.Approval)
	}
}

func TestComposerTextDoesNotApproveOrApply(t *testing.T) {
	m := newModel(Options{Demo: true})
	typed, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got := typed.(model)
	if got.snap.Approval == nil || got.input.Value() != "y" {
		t.Fatalf("composer y changed the approval: value %q approval %+v", got.input.Value(), got.snap.Approval)
	}
	got.input.SetValue("y")
	sent, cmd := got.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("enter started a command")
	}
	got = sent.(model)
	if got.snap.Approval == nil || !strings.Contains(got.snap.Notice, "was not sent") {
		t.Fatalf("enter approved: notice %q approval %+v", got.snap.Notice, got.snap.Approval)
	}

	got.snap.Digest = exampleDigest
	got.snap.View = "apply"
	got.focus = focusInput
	again, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got = again.(model)
	if got.applied || got.snap.Digest != exampleDigest || got.input.Value() != "y" {
		t.Fatalf("composer y applied digest %q applied %v", got.snap.Digest, got.applied)
	}
	got.focus = focusApply
	confirmed, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got = confirmed.(model)
	if !got.applied || !strings.Contains(got.snap.Notice, exampleDigest) {
		t.Fatalf("focused confirm did not keep the digest: %q", got.snap.Notice)
	}
}

func TestShortcutsStayOnTheFocusedComponent(t *testing.T) {
	m := newModel(Options{Demo: true})
	focused, _ := m.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	got := focused.(model)
	if got.focus != focusApproval {
		t.Fatalf("focus %d", got.focus)
	}
	tools, _ := got.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	got = tools.(model)
	if got.focus != focusTools {
		t.Fatalf("focus %d", got.focus)
	}
	typed, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	got = typed.(model)
	if got.snap.Approval == nil || got.input.Value() != "" {
		t.Fatalf("tools focus typed or approved: value %q approval %+v", got.input.Value(), got.snap.Approval)
	}
	pasted, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y\nno"), Paste: true})
	got = pasted.(model)
	if got.input.Value() != "" || got.snap.Approval == nil {
		t.Fatalf("paste while tools were focused: %q", got.input.Value())
	}
}

func TestPasteKeepsLinesAndUnicode(t *testing.T) {
	m := newModel(Options{})
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("line one\nline two — キー"), Paste: true}
	next, _ := m.Update(msg)
	got := next.(model)
	if got.input.Value() != "line one\nline two — キー" {
		t.Fatalf("paste %q", got.input.Value())
	}
	if got.snap.Approval != nil {
		t.Fatal("paste approved an action")
	}
}

func TestLiveSessionIgnoresDemoSceneKeys(t *testing.T) {
	m := newModel(Options{})
	m.snap.Messages = []Message{{Role: "you", Text: "keep this run"}}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}, Alt: true})
	got := next.(model)
	if len(got.snap.Messages) != 1 || got.snap.Messages[0].Text != "keep this run" || got.snap.Scene != "" {
		t.Fatalf("scene changed %+v", got.snap)
	}
	demo := newModel(Options{Demo: true})
	switched, _ := demo.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}, Alt: true})
	if switched.(model).snap.Scene != "composer" {
		t.Fatalf("demo scene %q", switched.(model).snap.Scene)
	}
}

func TestShowIDsRevealsTheFullHashAndDigest(t *testing.T) {
	frames := ExampleFrames(96, false)
	approval := frames[2].Snapshot
	approval.ShowIDs = true
	if !strings.Contains(Render(approval), exampleHash) {
		t.Fatal(Render(approval))
	}
	patch := frames[3].Snapshot
	patch.ShowIDs = true
	if !strings.Contains(Render(patch), exampleDigest) {
		t.Fatal(Render(patch))
	}
	apply := patch
	apply.View = "apply"
	apply.ShowIDs = false
	shown := Render(apply)
	if !strings.Contains(shown, exampleDigest) {
		t.Fatal(shown)
	}
}

func TestScenesDoNotShareState(t *testing.T) {
	frames := ExampleFrames(96, false)
	frames[5].Snapshot.Tools[0].Detail = "changed"
	frames[5].Snapshot.Approval.Path = "other.py"
	if frames[2].Snapshot.Tools[0].Detail == "changed" || frames[2].Snapshot.Approval.Path != "tests/test_retry.py" {
		t.Fatal("narrow scene shared the approval scene")
	}
	if frames[1].Snapshot.Messages[0].Text == frames[4].Snapshot.Messages[0].Text {
		t.Fatal("stream and plan are the same prompt")
	}
}

func TestUntrustedControlsStayOutOfTheFrame(t *testing.T) {
	s := Snapshot{
		Width: 48, Height: 24,
		Messages: []Message{{Role: "agt", Text: "ok\x1b[31mred\x1b[0m\x1b]0;title\x07\nキー"}},
		Tools:    []ToolLine{{Name: "search_code", Output: "bell\x07here", Expanded: true}},
	}
	out := Render(s)
	if strings.Contains(out, "\x1b") || strings.Contains(out, "\x07") {
		t.Fatalf("control sequence reached the frame: %q", out)
	}
	if !strings.Contains(out, "okred") || !strings.Contains(out, "キー") || !strings.Contains(out, "bellhere") {
		t.Fatal(out)
	}
	for _, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > 48 {
			t.Fatalf("line is %d > 48: %q", lipgloss.Width(line), line)
		}
	}
	plain := Snapshot{Width: 120, Height: 24, BudgetCents: 0}
	if !strings.Contains(Render(plain), "estimate $0.00") {
		t.Fatal(Render(plain))
	}
}

func TestRepeatedEventDoesNotDuplicateText(t *testing.T) {
	m := newModel(Options{Demo: true})
	m.gen = 2
	m.snap.Messages = nil
	stale := eventMsg{gen: 1, ev: client.Event{Seq: 1, Type: "assistant.delta", Data: []byte(`{"text":"again"}`)}}
	next, cmd := m.Update(stale)
	got := next.(model)
	if len(got.snap.Messages) != 0 || cmd != nil {
		t.Fatalf("stale generation applied %+v", got.snap.Messages)
	}
	live := eventMsg{gen: 2, ev: client.Event{Seq: 3, Type: "assistant.delta", Data: []byte(`{"text":"once"}`)}}
	next, _ = got.Update(live)
	got = next.(model)
	next, _ = got.Update(live)
	got = next.(model)
	if len(got.snap.Messages) != 1 || got.snap.Messages[0].Text != "once" {
		t.Fatalf("%+v", got.snap.Messages)
	}
}

func TestRestoreTerminalShowsTheCursor(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	restoreTerminal()
	_ = w.Close()
	os.Stdout = old
	body, _ := io.ReadAll(r)
	_ = r.Close()
	out := string(body)
	if !strings.Contains(out, "\x1b[?1049l") || !strings.Contains(out, "\x1b[?25h") || !strings.Contains(out, "\x1b[0m") {
		t.Fatalf("restore %q", out)
	}
}
