package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestSessionsKeepRunsOrderedAndRetrySafe(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	repo := t.TempDir()
	other := t.TempDir()
	sess, err := st.CreateSession(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RequireRepo(ctx, sess.ID, other); !errors.Is(err, store.ErrRepoMismatch) {
		t.Fatalf("mismatch %v", err)
	}
	kept, err := st.GetSession(ctx, sess.ID)
	if err != nil || kept.RepoPath != repo {
		t.Fatalf("redirected session %+v err %v", kept, err)
	}
	listed, err := st.ListSessions(ctx, repo, 20)
	if err != nil || len(listed) != 1 || listed[0].ID != sess.ID {
		t.Fatalf("list %+v err %v", listed, err)
	}
	if otherListed, err := st.ListSessions(ctx, other, 20); err != nil || len(otherListed) != 0 {
		t.Fatalf("other repo %+v err %v", otherListed, err)
	}

	legacy := mustCreate(t, st, "hash-legacy", "k-legacy")
	if legacy.SessionID != "" {
		t.Fatalf("standalone session %q", legacy.SessionID)
	}
	if err := st.RecordSessionResult(ctx, legacy); err != nil {
		t.Fatal(err)
	}

	first, err := st.Submit(ctx, sessionSubmit(sess, "msg_1", "fix the retry"))
	if err != nil || !first.Created || first.Run.Goal != "fix the retry" || first.Run.SessionID != sess.ID {
		t.Fatalf("first %+v err %v", first, err)
	}
	replay, err := st.Submit(ctx, sessionSubmit(sess, "msg_1", "a different goal"))
	if err != nil || replay.Created || replay.Run.ID != first.Run.ID || replay.Run.Goal != "fix the retry" {
		t.Fatalf("replay %+v err %v", replay, err)
	}
	if _, err := st.Submit(ctx, sessionSubmit(sess, "msg 2", "spaced")); err == nil {
		t.Fatal("a spaced client id was stored")
	}
	busyIn := sessionSubmit(sess, "msg_2", "do not rewrite the goal")
	busyIn.Create.RepoPath = other
	if _, err := st.Submit(ctx, busyIn); !errors.Is(err, store.ErrRepoMismatch) {
		t.Fatalf("submit mismatch %v", err)
	}
	if _, err := st.Submit(ctx, sessionSubmit(sess, "msg_2", "do not rewrite the goal")); !errors.Is(err, store.ErrSessionBusy) {
		t.Fatalf("busy %v", err)
	}
	current, err := st.GetRun(ctx, first.Run.ID)
	if err != nil || current.Goal != "fix the retry" {
		t.Fatalf("goal changed: %+v err %v", current.Goal, err)
	}
	msgs, err := st.ListSessionMessages(ctx, sess.ID)
	if err != nil || len(msgs) != 1 || msgs[0].ClientMessageID != "msg_1" || msgs[0].Seq != 1 {
		t.Fatalf("messages %+v err %v", msgs, err)
	}

	raceSess, err := st.CreateSession(ctx, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]store.SubmitResult, 8)
	errs := make([]error, 8)
	wg.Add(len(results))
	for i := range results {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = st.Submit(ctx, sessionSubmit(raceSess, "msg_race", "race once"))
		}(i)
	}
	close(start)
	wg.Wait()
	created := 0
	for i, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
		if results[i].Created {
			created++
		}
		if results[i].Run.ID != results[0].Run.ID || results[i].Run.Goal != "race once" {
			t.Fatalf("race %+v", results[i].Run)
		}
	}
	if created != 1 {
		t.Fatalf("created %d runs for one client message id", created)
	}
	raceMsgs, err := st.ListSessionMessages(ctx, raceSess.ID)
	if err != nil || len(raceMsgs) != 1 {
		t.Fatalf("race messages %+v err %v", raceMsgs, err)
	}

	if err := st.SetResult(ctx, first.Run.ID, map[string]any{
		"outcome": "repaired", "summary": "verification passed", "model_name": "local-repair",
		"diff": "--- a/payments/processor.py\n+++ b/payments/processor.py\n",
	}, ""); err != nil {
		t.Fatal(err)
	}
	firstRun := walk(t, st, current, run.Contextualising, run.Planning, run.Verifying, run.Committing, run.Succeeded)
	if err := st.RecordSessionResult(ctx, firstRun); err != nil {
		t.Fatal(err)
	}
	pending, err := st.PendingPatches(ctx, sess.ID)
	if err != nil || len(pending) != 1 || pending[0].RunID != firstRun.ID || pending[0].Change != "verified" {
		t.Fatalf("pending %+v err %v", pending, err)
	}
	if pending[0].Note == "" || !containsAll(pending[0].Note, "DATA origin=pending-patch", "absent from the new snapshot", "was not applied", "was not discarded") {
		t.Fatalf("note %q", pending[0].Note)
	}
	secondIn := sessionSubmit(sess, "msg_2", "start from a fresh snapshot")
	secondIn.Pending = pending
	second, err := st.Submit(ctx, secondIn)
	if err != nil || !second.Created || second.Run.ID == firstRun.ID || second.Run.Goal != "start from a fresh snapshot" {
		t.Fatalf("second %+v err %v", second, err)
	}
	if again, err := st.Submit(ctx, secondIn); err != nil || again.Created || again.Run.ID != second.Run.ID {
		t.Fatalf("second replay %+v err %v", again, err)
	}
	msgs, err = st.ListSessionMessages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []int64
	sawPending := false
	for _, msg := range msgs {
		seqs = append(seqs, msg.Seq)
		if msg.Kind == "pending" && msg.RunID == firstRun.ID && containsAll(msg.Content, "absent from the new snapshot", "was not discarded") {
			sawPending = true
		}
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("unordered %v", seqs)
		}
	}
	if !sawPending {
		t.Fatalf("messages %+v", msgs)
	}
	if _, err := st.AppendEvent(ctx, firstRun.ID, "patch.applied", "user", "local", map[string]any{"digest": "abc"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordApplied(ctx, firstRun, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordApplied(ctx, firstRun, "abc"); err != nil {
		t.Fatal(err)
	}
	pending, err = st.PendingPatches(ctx, sess.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("applied patch still pending %+v err %v", pending, err)
	}
	msgs, err = st.ListSessionMessages(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	appliedNotes := 0
	for _, msg := range msgs {
		if msg.ClientMessageID == "applied:"+firstRun.ID && containsAll(msg.Content, "change=applied", "applied=true", "digest=abc") {
			appliedNotes++
		}
	}
	if appliedNotes != 1 {
		t.Fatalf("applied notes %d in %+v", appliedNotes, msgs)
	}

	answered, err := st.CreateSession(ctx, repo, "questions")
	if err != nil {
		t.Fatal(err)
	}
	ask, err := st.Submit(ctx, sessionSubmit(answered, "msg_ask", "what does charge do"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetResult(ctx, ask.Run.ID, map[string]any{"outcome": "answered", "summary": "one ledger row", "diff": "not a patch"}, ""); err != nil {
		t.Fatal(err)
	}
	walk(t, st, ask.Run, run.Contextualising, run.Planning, run.Verifying, run.Committing, run.Succeeded)
	pending, err = st.PendingPatches(ctx, answered.ID)
	if err != nil || len(pending) != 0 {
		t.Fatalf("answer was pending %+v err %v", pending, err)
	}
}

func TestUsageKeepsTheConfiguredModelName(t *testing.T) {
	st := testutil.Open(t)
	ctx := context.Background()
	created := mustCreate(t, st, "hash-model", "k-model")
	current := walk(t, st, created, run.Contextualising, run.Planning)
	if _, err := st.CommitModelIntent(ctx, current.ID, current.StateVersion, store.Intent{
		Kind: "finish", Text: "done", Model: "fake", ModelName: "local-repair",
		InputTokens: 4, OutputTokens: 2, CostMicros: 9,
	}); err != nil {
		t.Fatal(err)
	}
	usage, err := st.ListUsage(ctx, current.ID)
	if err != nil || len(usage) == 0 {
		t.Fatalf("usage %+v err %v", usage, err)
	}
	for _, row := range usage {
		if row.ModelName != "local-repair" {
			t.Fatalf("usage model %q", row.ModelName)
		}
	}
	events, err := st.EventsAfter(ctx, current.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, ev := range events {
		if ev.Type != "model.called" {
			continue
		}
		var payload struct {
			ModelName string `json:"model_name"`
		}
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.ModelName == "local-repair" {
			saw = true
		}
		if payload.ModelName == "fake" {
			t.Fatalf("provider stored as model name in %s", ev.Payload)
		}
	}
	if !saw {
		t.Fatalf("events %+v", events)
	}

	empty := mustCreate(t, st, "hash-empty-model", "k-empty-model")
	empty = walk(t, st, empty, run.Contextualising, run.Planning)
	if _, err := st.CommitModelIntent(ctx, empty.ID, empty.StateVersion, store.Intent{
		Kind: "finish", Text: "done", Model: "fake", InputTokens: 1, OutputTokens: 1,
	}); err != nil {
		t.Fatal(err)
	}
	usage, err = st.ListUsage(ctx, empty.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range usage {
		if row.ModelName != "" {
			t.Fatalf("provider stored as model %q", row.ModelName)
		}
	}
}

func sessionSubmit(sess store.Session, msgID, content string) store.SubmitInput {
	return store.SubmitInput{
		SessionID: sess.ID, ClientMessageID: msgID, Content: content,
		Create: store.CreateInput{
			Goal: content, RepoPath: sess.RepoPath, BudgetCents: 100, MaxSteps: 4,
			WallSeconds: 60, Provider: "fake", RequestHash: "hash-" + msgID, Interaction: "agent",
		},
	}
}

func walk(t *testing.T, st *store.Store, current store.Run, states ...run.State) store.Run {
	t.Helper()
	var err error
	for _, next := range states {
		current, err = st.Transition(context.Background(), current.ID, current.StateVersion, next, "test")
		if err != nil {
			t.Fatalf("%s -> %s: %v", current.State, next, err)
		}
	}
	return current
}

func containsAll(s string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}
