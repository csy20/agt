package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/cli"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/testutil"
	"github.com/csy20/agt/internal/workspace"
)

func TestSessionsReopenWithoutReplayingWork(t *testing.T) {
	st := testutil.Open(t)
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake", DataDir: t.TempDir()}, testToken).Handler())
	t.Cleanup(ts.Close)
	t.Setenv("AGT_TOKEN", testToken)

	real := t.TempDir()
	writeRepoFile(t, filepath.Join(real, "a.txt"), "old\n", 0o644)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	canonical, err := workspace.CanonicalRepo(real)
	if err != nil {
		t.Fatal(err)
	}
	created := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions", map[string]any{"repo_path": link, "title": "retry"}, ""))
	if created.status != http.StatusCreated {
		t.Fatalf("create %d %s", created.status, created.raw)
	}
	sess := created.body["session"].(map[string]any)
	sessionID, _ := sess["id"].(string)
	if sess["repo_path"] != canonical {
		t.Fatalf("stored repo %#v want %s", sess["repo_path"], canonical)
	}
	listed := getJSON(t, ts.URL+"/v1/sessions?repo_path="+url.QueryEscape(canonical))
	rawListed, _ := json.Marshal(listed)
	if !strings.Contains(string(rawListed), sessionID) {
		t.Fatalf("list %s", rawListed)
	}
	opened := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions/"+sessionID+"/open", map[string]any{"repo_path": real}, ""))
	if opened.status != http.StatusOK || opened.body["active_run_id"] != "" {
		t.Fatalf("open %#v", opened.body)
	}
	other := t.TempDir()
	mismatch := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions/"+sessionID+"/open", map[string]any{"repo_path": other}, ""))
	if mismatch.status != http.StatusConflict || !strings.Contains(mismatch.raw, canonical) || !strings.Contains(mismatch.raw, "was not redirected") {
		t.Fatalf("mismatch %d %s", mismatch.status, mismatch.raw)
	}
	kept, err := st.GetSession(context.Background(), sessionID)
	if err != nil || kept.RepoPath != canonical {
		t.Fatalf("repo changed to %s err %v", kept.RepoPath, err)
	}

	standalone := postRun(t, ts.URL, map[string]any{
		"goal": "standalone", "repo_path": real, "provider": "fake",
		"script_path": filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json"),
		"profile":     pythonProfile(), "max_steps": 0,
	}, "idem-standalone")
	if standalone.status != http.StatusAccepted {
		t.Fatalf("standalone %d %s", standalone.status, standalone.body)
	}
	view := getJSON(t, ts.URL+"/v1/runs/"+standalone.runID)
	if view["session_id"] != nil || view["goal"] != "standalone" {
		t.Fatalf("standalone view %#v", view)
	}
	loaded, err := st.GetRun(context.Background(), standalone.runID)
	if err != nil || loaded.SessionID != "" {
		t.Fatalf("legacy session %q err %v", loaded.SessionID, err)
	}

	script := filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json")
	first := submitSession(t, ts.URL, sessionID, map[string]any{
		"goal": "fix the retry", "content": "fix the retry", "repo_path": link,
		"provider": "fake", "script_path": script, "profile": pythonProfile(),
		"client_message_id": "msg_1", "max_steps": 0, "interaction": "agent",
	})
	if first.status != http.StatusAccepted || !first.created || first.runID == "" {
		t.Fatalf("submit %+v", first)
	}
	replay := submitSession(t, ts.URL, sessionID, map[string]any{
		"goal": "rewritten", "content": "rewritten", "repo_path": real,
		"provider": "fake", "script_path": script, "profile": pythonProfile(),
		"client_message_id": "msg_1", "max_steps": 0,
	})
	if replay.status != http.StatusOK || replay.created || replay.runID != first.runID {
		t.Fatalf("replay %+v", replay)
	}
	busy := submitSession(t, ts.URL, sessionID, map[string]any{
		"goal": "do not rewrite", "repo_path": real, "provider": "fake",
		"script_path": script, "profile": pythonProfile(), "client_message_id": "msg_2",
	})
	if busy.status != http.StatusConflict || busy.runID != "" || !strings.Contains(busy.raw, first.runID) || !strings.Contains(busy.raw, "was not changed") {
		t.Fatalf("busy %+v", busy)
	}
	current, err := st.GetRun(context.Background(), first.runID)
	if err != nil || current.Goal != "fix the retry" || current.SessionID != sessionID || current.Interaction != "agent" {
		t.Fatalf("run %+v err %v", current, err)
	}
	msgs, err := st.ListSessionMessages(context.Background(), sessionID)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("messages %+v err %v", msgs, err)
	}

	current, err = st.Transition(context.Background(), current.ID, current.StateVersion, run.Paused, "test")
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := captureCLI(t, func() int {
		return cli.Run([]string{"agt", "session", "open", sessionID, "--server", ts.URL, "--repo", link})
	})
	if code != 1 || !strings.Contains(out, "attaching. This does not start a run or apply a patch.") || !strings.Contains(out, first.runID) {
		t.Fatalf("cli open %d\n%s\n%s", code, out, errOut)
	}
	runs, err := st.ListSessionRuns(context.Background(), sessionID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("open created runs %+v err %v", runs, err)
	}
	code, out, errOut = captureCLI(t, func() int {
		return cli.Run([]string{"agt", "session", "show", sessionID, "--server", ts.URL, "--repo", real})
	})
	if code != 0 || !strings.Contains(out, "fix the retry") || strings.Contains(out, "attaching") || !strings.Contains(errOut, "No run was started. Patches were not applied.") {
		t.Fatalf("cli show %d\n%s\n%s", code, out, errOut)
	}
	code, out, errOut = captureCLI(t, func() int {
		return cli.Run([]string{"agt", "session", "submit", sessionID, "--server", ts.URL, "--repo", real, "--goal", "rewritten", "--message-id", "msg_1", "--provider", "fake", "--script", script, "--profile", "python", "--test-arg", "python", "--test-arg", "-m", "--test-arg", "unittest", "--test-arg", "tests.test_retry"})
	})
	if code != 0 || !strings.Contains(out, first.runID) || !strings.Contains(out, "did not create another run") {
		t.Fatalf("cli replay %d\n%s\n%s", code, out, errOut)
	}
	code, _, errOut = captureCLI(t, func() int {
		return cli.Run([]string{"agt", "session", "open", sessionID, "--server", ts.URL, "--repo", other})
	})
	if code == 0 || !strings.Contains(errOut, "was not redirected") {
		t.Fatalf("cli mismatch %d\n%s", code, errOut)
	}
	if kept, err = st.GetSession(context.Background(), sessionID); err != nil || kept.RepoPath != canonical {
		t.Fatalf("cli redirected %s", kept.RepoPath)
	}

	if err := st.SetResult(context.Background(), current.ID, map[string]any{
		"outcome": "repaired", "summary": "verification passed", "model_name": "local-repair",
		"diff": "--- a/a.txt\n+++ b/a.txt\n",
	}, ""); err != nil {
		t.Fatal(err)
	}
	current = advance(t, st, current, run.Planning, run.Verifying, run.Committing, run.Succeeded)
	code, out, errOut = captureCLI(t, func() int {
		return cli.Run([]string{"agt", "session", "open", sessionID, "--server", ts.URL, "--repo", real})
	})
	if code != 0 || strings.Contains(out, "attaching") || !strings.Contains(out, "fix the retry") || !strings.Contains(errOut, "No run was started") {
		t.Fatalf("cli completed %d\n%s\n%s", code, out, errOut)
	}
	second := submitSession(t, ts.URL, sessionID, map[string]any{
		"goal": "fresh snapshot", "repo_path": real, "provider": "fake",
		"script_path": script, "profile": pythonProfile(), "client_message_id": "msg_2",
	})
	if second.status != http.StatusAccepted || !second.created || second.runID == first.runID || !strings.Contains(second.raw, "absent from") || !strings.Contains(second.notice, "was not discarded") {
		t.Fatalf("second %+v", second)
	}
	fresh, err := st.GetRun(context.Background(), first.runID)
	if err != nil || fresh.Goal != "fix the retry" {
		t.Fatalf("first goal %q", fresh.Goal)
	}
	read := getJSON(t, ts.URL+"/v1/sessions/"+sessionID+"?repo_path="+url.QueryEscape(canonical))
	messages, _ := read["messages"].([]any)
	var seqs []float64
	sawPending := false
	for _, item := range messages {
		msg := item.(map[string]any)
		seqs = append(seqs, msg["seq"].(float64))
		content, _ := msg["content"].(string)
		if msg["kind"] == "pending" && strings.Contains(content, "absent from the new snapshot") && strings.Contains(content, "was not discarded") {
			sawPending = true
		}
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("unordered %v", seqs)
		}
	}
	if !sawPending {
		t.Fatalf("session %#v", read)
	}
	pending, _ := read["pending"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending %#v", read["pending"])
	}
	runs, err = st.ListSessionRuns(context.Background(), sessionID)
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs %d", len(runs))
	}

	ask := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions", map[string]any{"repo_path": real}, ""))
	askID := ask.body["session"].(map[string]any)["id"].(string)
	asked := submitSession(t, ts.URL, askID, map[string]any{
		"goal": "what does charge do?", "repo_path": real, "provider": "fake",
		"script_path": script, "profile": pythonProfile(), "client_message_id": "msg_ask",
		"interaction": "ask",
	})
	if asked.status != http.StatusAccepted {
		t.Fatalf("ask %+v", asked)
	}
	askedRun, err := st.GetRun(context.Background(), asked.runID)
	if err != nil || askedRun.Interaction != "ask" {
		t.Fatalf("interaction %s", askedRun.Interaction)
	}
}

func TestSessionApplyKeepsThePatchPendingUntilTheDigestMatches(t *testing.T) {
	st := testutil.Open(t)
	data := t.TempDir()
	repo := t.TempDir()
	writeRepoFile(t, filepath.Join(repo, "a.txt"), "old\n", 0o644)
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake", DataDir: data}, testToken).Handler())
	t.Cleanup(ts.Close)
	created := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions", map[string]any{"repo_path": repo}, ""))
	if created.status != http.StatusCreated {
		t.Fatalf("session %d %s", created.status, created.raw)
	}
	sessionID := created.body["session"].(map[string]any)["id"].(string)
	script := filepath.Join(testutil.RepoRoot(t), "eval", "scripts", "fix_retry.json")
	submitted := submitSession(t, ts.URL, sessionID, map[string]any{
		"goal": "edit a", "repo_path": repo, "provider": "fake", "script_path": script,
		"profile": pythonProfile(), "client_message_id": "msg_edit",
	})
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submit %+v", submitted)
	}
	current, err := st.GetRun(context.Background(), submitted.runID)
	if err != nil {
		t.Fatal(err)
	}
	work, _, err := workspace.Materialize(current.RepoPath, data, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(work, "a.txt"), "new\n", 0o644)
	if err := st.SetWorktree(context.Background(), current.ID, work); err != nil {
		t.Fatal(err)
	}
	if err := st.SetResult(context.Background(), current.ID, map[string]any{
		"outcome": "repaired", "summary": "verification passed", "diff": "placeholder",
	}, ""); err != nil {
		t.Fatal(err)
	}
	current = advance(t, st, current, run.Contextualising, run.Planning, run.Verifying, run.Committing, run.Succeeded)
	diff := getJSON(t, ts.URL+"/v1/runs/"+current.ID+"/diff")
	digest, _ := diff["digest"].(string)
	if digest == "" || !strings.Contains(diff["diff"].(string), "new") {
		t.Fatalf("diff %#v", diff)
	}
	read := getJSON(t, ts.URL+"/v1/sessions/"+sessionID)
	pending, _ := read["pending"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["change"] != "verified" || pending[0].(map[string]any)["digest"] != digest {
		t.Fatalf("pending %#v", read["pending"])
	}
	note, _ := pending[0].(map[string]any)["note"].(string)
	if !strings.Contains(note, "absent from the new snapshot") || !strings.Contains(note, digest) {
		t.Fatalf("note %s", note)
	}
	wrong := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": "deadbeef"}, "")
	wrongBody := readBody(t, wrong)
	if wrong.status != http.StatusConflict || !strings.Contains(wrongBody, "digest") {
		t.Fatalf("wrong digest %d %s", wrong.status, wrongBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "old\n" {
		t.Fatalf("wrong digest wrote %q", got)
	}
	ok := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": digest}, "")
	okBody := readBody(t, ok)
	if ok.status != http.StatusOK {
		t.Fatalf("apply %d %s", ok.status, okBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "new\n" {
		t.Fatalf("applied %q", got)
	}
	read = getJSON(t, ts.URL+"/v1/sessions/"+sessionID)
	if pending, _ = read["pending"].([]any); len(pending) != 0 {
		t.Fatalf("still pending %#v", read["pending"])
	}
	raw, _ := json.Marshal(read["messages"])
	if !strings.Contains(string(raw), "change=applied") || !strings.Contains(string(raw), digest) {
		t.Fatalf("messages %s", raw)
	}
	writeRepoFile(t, filepath.Join(repo, "a.txt"), "newer\n", 0o644)
	stale := postJSON(t, ts.URL+"/v1/runs/"+current.ID+"/apply", map[string]any{"digest": digest}, "")
	staleBody := readBody(t, stale)
	if stale.status != http.StatusConflict || !strings.Contains(staleBody, "a.txt") {
		t.Fatalf("stale %d %s", stale.status, staleBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "a.txt")); got != "newer\n" {
		t.Fatalf("stale overwrote %q", got)
	}
}

func TestSessionSurvivesDaemonRestart(t *testing.T) {
	t.Setenv("AGT_TOKEN", testToken)
	root := testutil.RepoRoot(t)
	binDir := t.TempDir()
	agtdBin := buildBin(t, root, binDir, "agtd", "./cmd/agtd")
	agtBin := buildBin(t, root, binDir, "agt", "./cmd/agt")
	st := testutil.Open(t)
	repo := testutil.Fixture(t)
	addr := freeAddr(t)
	data := t.TempDir()
	var current *exec.Cmd
	t.Cleanup(func() { stopProcess(current) })
	current = startAgtd(t, agtdBin, data, addr)
	base := "http://" + addr
	created := decodeJSON(t, postJSON(t, base+"/v1/sessions", map[string]any{"repo_path": repo, "title": "restart"}, ""))
	if created.status != http.StatusCreated {
		t.Fatalf("session %d %s", created.status, created.raw)
	}
	sessionID := created.body["session"].(map[string]any)["id"].(string)
	submitted := submitSession(t, base, sessionID, map[string]any{
		"goal": "restart me", "repo_path": repo, "provider": "fake",
		"script_path":       filepath.Join(root, "eval", "scripts", "fix_retry.json"),
		"profile":           pythonProfile(),
		"client_message_id": "msg_restart",
		"max_steps":         0,
	})
	if submitted.status != http.StatusAccepted {
		t.Fatalf("submit %+v", submitted)
	}
	waitHTTPState(t, base, submitted.runID, "PAUSED_BUDGET")
	stopProcess(current)
	current = nil
	current = startAgtd(t, agtdBin, data, addr)

	opened := decodeJSON(t, postJSON(t, base+"/v1/sessions/"+sessionID+"/open", map[string]any{"repo_path": repo}, ""))
	if opened.status != http.StatusOK || opened.body["active_run_id"] != submitted.runID {
		t.Fatalf("reopen %#v", opened.body)
	}
	replay := submitSession(t, base, sessionID, map[string]any{
		"goal": "restart me", "repo_path": repo, "provider": "fake",
		"script_path":       filepath.Join(root, "eval", "scripts", "fix_retry.json"),
		"profile":           pythonProfile(),
		"client_message_id": "msg_restart",
		"max_steps":         0,
	})
	if replay.status != http.StatusOK || replay.created || replay.runID != submitted.runID {
		t.Fatalf("replay %+v", replay)
	}
	busy := submitSession(t, base, sessionID, map[string]any{
		"goal": "different", "repo_path": repo, "provider": "fake",
		"script_path":       filepath.Join(root, "eval", "scripts", "fix_retry.json"),
		"profile":           pythonProfile(),
		"client_message_id": "msg_other",
		"max_steps":         0,
	})
	if busy.status != http.StatusConflict || !strings.Contains(busy.raw, submitted.runID) {
		t.Fatalf("busy %+v", busy)
	}
	runs, err := st.ListSessionRuns(context.Background(), sessionID)
	if err != nil || len(runs) != 1 || runs[0].Goal != "restart me" || runs[0].State != run.PausedBudget {
		t.Fatalf("runs %+v err %v", runs, err)
	}
	cmd := exec.Command(agtBin, "session", "open", sessionID, "--repo", repo, "--server", base)
	cmd.Env = append(os.Environ(), "AGT_TOKEN="+testToken)
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err == nil || !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("attach exit %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "attaching. This does not start a run or apply a patch.") || !strings.Contains(text, submitted.runID) || !strings.Contains(text, "PAUSED_BUDGET") {
		t.Fatalf("attach\n%s", text)
	}
	show := exec.Command(agtBin, "session", "show", sessionID, "--repo", repo, "--server", base)
	show.Env = append(os.Environ(), "AGT_TOKEN="+testToken)
	showOut, err := show.CombinedOutput()
	if err != nil {
		t.Fatalf("show %v\n%s", err, showOut)
	}
	if !strings.Contains(string(showOut), "restart me") || !strings.Contains(string(showOut), "No run was started") {
		t.Fatalf("show\n%s", showOut)
	}
}

type jsonResp struct {
	status int
	raw    string
	body   map[string]any
}

func decodeJSON(t *testing.T, resp response) jsonResp {
	t.Helper()
	defer resp.body.Close()
	raw, err := io.ReadAll(resp.body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return jsonResp{status: resp.status, raw: string(raw), body: body}
}

type submission struct {
	status  int
	raw     string
	runID   string
	created bool
	notice  string
}

func submitSession(t *testing.T, base, sessionID string, body map[string]any) submission {
	t.Helper()
	resp := postJSON(t, base+"/v1/sessions/"+sessionID+"/messages", body, "")
	decoded := decodeJSON(t, resp)
	runID, _ := decoded.body["run_id"].(string)
	created, _ := decoded.body["created"].(bool)
	notice, _ := decoded.body["notice"].(string)
	return submission{status: decoded.status, raw: decoded.raw, runID: runID, created: created, notice: notice}
}

func captureCLI(t *testing.T, fn func() int) (int, string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC := make(chan string, 1)
	errC := make(chan string, 1)
	go func() {
		body, _ := io.ReadAll(outR)
		outC <- string(body)
	}()
	go func() {
		body, _ := io.ReadAll(errR)
		errC <- string(body)
	}()
	code := fn()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = outW.Close()
	_ = errW.Close()
	return code, <-outC, <-errC
}
