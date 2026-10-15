package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/csy20/agt/internal/client"
)

func TestPickerAndPendingDoNotStartWork(t *testing.T) {
	m := newModel(Options{Repo: t.TempDir()})
	empty, _ := m.showSessionList(nil)
	if empty.snap.View == "picker" || !strings.Contains(empty.snap.Notice, "No saved sessions") {
		t.Fatalf("empty %+v", empty.snap.Notice)
	}
	listed, _ := m.showSessionList([]client.Session{
		{ID: "ses_a", Title: "one"},
		{ID: "ses_b", Title: "two"},
	})
	if listed.snap.View != "picker" || len(listed.picker) != 3 || listed.snap.Picker[2] != "new session" {
		t.Fatalf("picker %+v", listed.snap.Picker)
	}
	moved, _ := listed.key(tea.KeyMsg{Type: tea.KeyDown})
	at := moved.(model)
	if at.snap.PickerAt != 1 {
		t.Fatalf("cursor %d", at.snap.PickerAt)
	}
	opened, cmd := at.key(tea.KeyMsg{Type: tea.KeyEnter})
	got := opened.(model)
	if cmd != nil || !strings.Contains(got.snap.Notice, "ses_b") || !strings.Contains(got.snap.Notice, "does not start a run") {
		t.Fatalf("open notice %q cmd %v", got.snap.Notice, cmd)
	}
	listed.snap.PickerAt = len(listed.picker) - 1
	fresh, cmd := listed.openPicker()
	got = fresh.(model)
	if cmd != nil || got.sessionID != "" || got.snap.RunID != "" || !strings.Contains(got.snap.Notice, "Nothing was applied") {
		t.Fatalf("new session %q id %q", got.snap.Notice, got.sessionID)
	}

	done, cmd := m.showSession(client.SessionView{
		Session: client.Session{ID: "ses_done", RepoPath: m.opts.Repo},
		Messages: []client.SessionMessage{
			{Seq: 1, Role: "user", Kind: "user", Content: "first goal"},
			{Seq: 2, Role: "note", Kind: "result", Content: "DATA origin=run-result\nchange=verified"},
		},
		Pending: []client.PendingPatch{{
			RunID: "run_old", Change: "verified",
			Note: "This patch is still pending. Its edits are absent from the new snapshot. It was not applied and it was not discarded.",
		}},
	})
	if cmd != nil || done.snap.RunID != "" || done.sessionID != "ses_done" {
		t.Fatalf("completed open started %s cmd %v", done.snap.RunID, cmd)
	}
	view := done.View()
	if !strings.Contains(view, "first goal") || !strings.Contains(view, "absent from the new snapshot") || !strings.Contains(view, "change=verified") {
		t.Fatalf("history view\n%s", view)
	}
	live, cmd := m.showSession(client.SessionView{
		Session:     client.Session{ID: "ses_live", RepoPath: m.opts.Repo},
		ActiveRunID: "run_live",
		Messages:    []client.SessionMessage{{Seq: 1, Role: "user", Content: "live goal"}},
	})
	if cmd != nil || live.snap.RunID != "run_live" || !strings.Contains(live.snap.Notice, "did not start a new run") {
		t.Fatalf("active %s %q cmd %v", live.snap.RunID, live.snap.Notice, cmd)
	}

	busy, _ := m.acceptSubmission(submissionMsg{
		err:  &client.BusyError{RunID: "run_live", Message: "a run is already active (run_live). This prompt was not stored and the run goal was not changed"},
		text: "do not rewrite", mode: "agent",
	})
	held := busy.(model)
	if held.input.Value() != "do not rewrite" || held.snap.RunID != "" || !strings.Contains(held.View(), "was not changed") {
		t.Fatalf("busy input %q run %q view %s", held.input.Value(), held.snap.RunID, held.View())
	}
	mismatch, _ := m.acceptSubmission(submissionMsg{
		err:  &client.RepoMismatchError{RepoPath: "/kept", Message: "session is bound to /kept and was not redirected"},
		text: "other repo",
	})
	if mismatch.(model).input.Value() != "other repo" {
		t.Fatal("mismatch cleared the composer")
	}

	m.snap.Pending = []PendingLine{{RunID: "run_old", Change: "verified"}}
	m.snap.RunID = ""
	diffed, cmd := m.slash("diff", "")
	shown := diffed.(model)
	if cmd != nil || !strings.Contains(shown.snap.Notice, "absent from the new snapshot") || !strings.Contains(shown.snap.Notice, "does not apply") {
		t.Fatalf("diff %q cmd %v", shown.snap.Notice, cmd)
	}
}
