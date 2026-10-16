package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/csy20/agt/internal/api"
	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/testutil"
	"github.com/csy20/agt/internal/verify"
)

func TestContractCreateRejectsClientPathsAndBadChecks(t *testing.T) {
	st := testutil.Open(t)
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake", DataDir: t.TempDir()}, testToken).Handler())
	t.Cleanup(ts.Close)
	repo := copyGateRepo(t, "featuregate")
	script := writeScript(t, []byte(`{"steps":[{"finish":true,"text":"idle","input_tokens":10,"output_tokens":5}]}`))
	absent := filepath.Join(t.TempDir(), "no-such-contract.json")
	base := func() map[string]any {
		return map[string]any{
			"goal": "make greet return hello", "repo_path": repo, "provider": "fake",
			"script_path": script, "profile": gateProfile(), "interaction": "agent",
		}
	}
	valid := featureContractMap()

	cases := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{"path string", func(b map[string]any) {
			b["task_type"] = "feature"
			b["contract"] = absent
		}, "object"},
		{"extra host path", func(b map[string]any) {
			b["task_type"] = "feature"
			b["contract"] = map[string]any{"version": "verify.v1", "path": "/etc/passwd", "checks": []any{}}
		}, "version and checks"},
		{"escaping path", func(b map[string]any) {
			b["task_type"] = "feature"
			b["contract"] = contractWithPath("../secrets.txt", "tests.test_accept")
		}, "escapes"},
		{"profile mismatch", func(b map[string]any) {
			b["task_type"] = "feature"
			raw := featureContractMap()
			checks := raw["checks"].([]any)
			checks[0].(map[string]any)["argv"] = []string{"node", "tests/test_regress.py"}
			b["contract"] = raw
		}, "python"},
		{"empty checks", func(b map[string]any) {
			b["task_type"] = "feature"
			b["contract"] = map[string]any{"version": "verify.v1", "checks": []any{}}
		}, "empty"},
		{"missing contract", func(b map[string]any) { b["task_type"] = "feature" }, "verification contract"},
		{"ask feature", func(b map[string]any) {
			b["interaction"] = "ask"
			b["task_type"] = "feature"
			b["contract"] = valid
		}, "read-only"},
		{"plan contract", func(b map[string]any) {
			b["interaction"] = "plan"
			b["contract"] = valid
		}, "read-only"},
		{"repair contract", func(b map[string]any) {
			b["task_type"] = "repair"
			b["contract"] = valid
		}, "repair"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := base()
			tc.edit(body)
			got := postRun(t, ts.URL, body, "idem-"+tc.name)
			if got.status != http.StatusBadRequest || !strings.Contains(got.body, tc.want) {
				t.Fatalf("status %d body %s", got.status, got.body)
			}
			if _, err := os.Stat(absent); err == nil {
				t.Fatal("daemon created the client path")
			}
		})
	}

	legacyBody := base()
	legacy := postRun(t, ts.URL, legacyBody, "idem-legacy")
	if legacy.status != http.StatusAccepted {
		t.Fatalf("legacy %d %s", legacy.status, legacy.body)
	}
	legacyView := getJSON(t, ts.URL+"/v1/runs/"+legacy.runID)
	if legacyView["task_type"] != "repair" || legacyView["contract_digest"] != nil && legacyView["contract_digest"] != "" || legacyView["contract"] != nil || legacyView["verification_outcome"] != nil && legacyView["verification_outcome"] != "" {
		t.Fatalf("legacy view %#v", legacyView)
	}

	featureBody := base()
	featureBody["task_type"] = "feature"
	featureBody["contract"] = valid
	created := postRun(t, ts.URL, featureBody, "idem-feature")
	if created.status != http.StatusAccepted {
		t.Fatalf("feature %d %s", created.status, created.body)
	}
	view := getJSON(t, ts.URL+"/v1/runs/"+created.runID)
	raw, _ := json.Marshal(view["contract"])
	if view["task_type"] != "feature" || view["contract_digest"] == "" || !strings.Contains(string(raw), "tests/test_accept.py") || strings.Contains(string(raw), absent) {
		t.Fatalf("feature view %#v", view)
	}
	if _, err := os.Stat(absent); err == nil {
		t.Fatal("stored contract was read from a host path")
	}
}

func TestFeatureSessionPreservesContractAndApplyDigest(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	ts := httptest.NewServer(api.New(st, config.Config{Provider: "fake", DataDir: eng.DataDir}, testToken).Handler())
	t.Cleanup(ts.Close)
	repo := copyGateRepo(t, "featuregate")
	original := readRepoFile(t, filepath.Join(repo, "greet.py"))
	if !strings.Contains(original, "return \"hi\"\n") {
		t.Fatalf("copy %q", original)
	}
	scriptDir := t.TempDir()
	impl := writeScriptAt(t, scriptDir, "impl.json", scriptSteps(
		patchStep("greet.py", "return \"hi\"\n", "return \"hello\"\n", "edit greet"),
		finishStep("greet now returns hello."),
	))
	idle := writeScriptAt(t, scriptDir, "idle.json", scriptSteps(finishStep("idle")))
	if strings.HasPrefix(impl, repo) || strings.HasPrefix(idle, repo) {
		t.Fatal("script is inside the repository")
	}

	created := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions", map[string]any{"repo_path": repo, "title": "feature"}, ""))
	if created.status != http.StatusCreated {
		t.Fatalf("session %d %s", created.status, created.raw)
	}
	sessionID, _ := created.body["session"].(map[string]any)["id"].(string)
	contract := featureContractMap()
	_, canonical, digest, err := verify.Parse(mustRaw(t, contract), verify.TaskFeature, "python")
	if err != nil {
		t.Fatal(err)
	}
	first := submitSession(t, ts.URL, sessionID, featureSubmit(repo, impl, "msg_feature", "make greet return hello", contract))
	if first.status != http.StatusAccepted || !first.created || first.runID == "" {
		t.Fatalf("submit %+v", first)
	}
	badRetry := submitSession(t, ts.URL, sessionID, featureSubmit(repo, impl, "msg_feature", "make greet return hello", "/tmp/weaker-contract.json"))
	if badRetry.status != http.StatusBadRequest {
		t.Fatalf("invalid retry %+v", badRetry)
	}
	weaker := map[string]any{
		"version": "verify.v1",
		"checks": []any{
			checkMap("reg", "regression", "tests.test_regress", "tests/test_regress.py"),
			checkMap("other", "acceptance", "tests.test_other", "tests/test_other.py"),
		},
	}
	replay := submitSession(t, ts.URL, sessionID, featureSubmit(repo, impl, "msg_feature", "rewritten goal", weaker))
	if replay.status != http.StatusOK || replay.created || replay.runID != first.runID {
		t.Fatalf("replay %+v", replay)
	}
	busy := submitSession(t, ts.URL, sessionID, featureSubmit(repo, idle, "msg_next", "another change", weaker))
	if busy.status != http.StatusConflict || !strings.Contains(busy.raw, first.runID) {
		t.Fatalf("busy %+v", busy)
	}
	stored, err := st.GetRun(context.Background(), first.runID)
	if err != nil || stored.Goal != "make greet return hello" || stored.ContractDigest != digest || stored.TaskType != "feature" {
		t.Fatalf("stored goal %q digest %s task %s err %v", stored.Goal, stored.ContractDigest, stored.TaskType, err)
	}
	if string(stored.ContractJSON) == "" || verify.Digest(canonical) != stored.ContractDigest {
		t.Fatal("create did not keep the canonical contract")
	}
	opened := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions/"+sessionID+"/open", map[string]any{"repo_path": repo}, ""))
	if opened.status != http.StatusOK {
		t.Fatalf("open %d %s", opened.status, opened.raw)
	}
	runs, err := st.ListSessionRuns(context.Background(), sessionID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %d err %v", len(runs), err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, first.runID); err != nil {
		testutil.Dump(t, st, first.runID)
		t.Fatal(err)
	}
	done, err := st.GetRun(context.Background(), first.runID)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != run.Succeeded || done.ContractDigest != digest || done.VerificationDigest == "" {
		testutil.Dump(t, st, first.runID)
		t.Fatalf("state %s digest %s verification %s err %s", done.State, done.ContractDigest, done.VerificationDigest, done.Error)
	}
	var result struct {
		Outcome            string `json:"outcome"`
		Summary            string `json:"summary"`
		VerificationDigest string `json:"verification_digest"`
	}
	if err := json.Unmarshal(done.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.Outcome != verify.OutcomeFeatureImplemented || result.VerificationDigest != done.VerificationDigest || strings.Contains(result.Summary, "tests passed") {
		t.Fatalf("result %+v", result)
	}
	if readRepoFile(t, filepath.Join(repo, "greet.py")) != original {
		t.Fatal("run wrote the source repository")
	}
	diff := getJSON(t, ts.URL+"/v1/runs/"+first.runID+"/diff")
	if diff["digest"] != done.VerificationDigest {
		t.Fatalf("diff digest %#v verification %s", diff["digest"], done.VerificationDigest)
	}
	shown := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions/"+sessionID+"/open", map[string]any{"repo_path": repo}, ""))
	if shown.status != http.StatusOK {
		t.Fatalf("reopen %d %s", shown.status, shown.raw)
	}
	runs, err = st.ListSessionRuns(context.Background(), sessionID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("reopen runs %d", len(runs))
	}
	pending, _ := shown.body["pending"].([]any)
	if len(pending) != 1 || pending[0].(map[string]any)["run_id"] != first.runID || pending[0].(map[string]any)["change"] != "verified" {
		t.Fatalf("pending %#v", shown.body["pending"])
	}

	second := submitSession(t, ts.URL, sessionID, featureSubmit(repo, idle, "msg_follow", "try a weaker contract", weaker))
	if second.status != http.StatusAccepted || !second.created || second.runID == first.runID {
		t.Fatalf("follow-up %+v", second)
	}
	if !strings.Contains(second.raw, "still pending") && !strings.Contains(second.notice, "still pending") {
		t.Fatalf("follow-up notice %+v", second)
	}
	if err := eng.Orch.Run(ctx, second.runID); err != nil {
		testutil.Dump(t, st, second.runID)
		t.Fatal(err)
	}
	follow, err := st.GetRun(context.Background(), second.runID)
	if err != nil {
		t.Fatal(err)
	}
	var followResult struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(follow.Result, &followResult); err != nil {
		t.Fatal(err)
	}
	if follow.State != run.FailedTerminal || followResult.Outcome != verify.OutcomeAlreadySatisfied || follow.ContractDigest == digest || follow.ContractDigest == "" {
		testutil.Dump(t, st, second.runID)
		t.Fatalf("follow state %s outcome %s digest %s", follow.State, followResult.Outcome, follow.ContractDigest)
	}
	if string(follow.BaselineJSON) == "" || string(follow.BaselineJSON) == "null" {
		t.Fatal("follow-up did not record a fresh baseline")
	}
	kept, err := st.GetRun(context.Background(), first.runID)
	if err != nil || kept.ContractDigest != digest || kept.Goal != "make greet return hello" {
		t.Fatalf("first run changed %+v", kept)
	}
	if readRepoFile(t, filepath.Join(repo, "greet.py")) != original {
		t.Fatal("follow-up wrote the source repository")
	}
	again := decodeJSON(t, postJSON(t, ts.URL+"/v1/sessions/"+sessionID+"/open", map[string]any{"repo_path": repo}, ""))
	pending, _ = again.body["pending"].([]any)
	found := false
	for _, item := range pending {
		row, _ := item.(map[string]any)
		if row["run_id"] == first.runID && row["outcome"] == verify.OutcomeFeatureImplemented {
			found = true
		}
	}
	if !found {
		t.Fatalf("first patch is not pending: %#v", again.body["pending"])
	}

	wrong := postJSON(t, ts.URL+"/v1/runs/"+first.runID+"/apply", map[string]any{"digest": "deadbeef"}, "")
	wrongBody := readBody(t, wrong)
	if wrong.status != http.StatusConflict || !strings.Contains(wrongBody, "digest") {
		t.Fatalf("wrong digest %d %s", wrong.status, wrongBody)
	}
	if readRepoFile(t, filepath.Join(repo, "greet.py")) != original {
		t.Fatal("wrong digest wrote the repository")
	}
	if err := st.SetVerification(context.Background(), first.runID, kept.FinalJSON, kept.VerificationOutcome, "not-the-artifact"); err != nil {
		t.Fatal(err)
	}
	mismatch := postJSON(t, ts.URL+"/v1/runs/"+first.runID+"/apply", map[string]any{"digest": done.VerificationDigest}, "")
	mismatchBody := readBody(t, mismatch)
	if mismatch.status != http.StatusConflict || !strings.Contains(mismatchBody, "verification digest") {
		t.Fatalf("verification digest %d %s", mismatch.status, mismatchBody)
	}
	if readRepoFile(t, filepath.Join(repo, "greet.py")) != original {
		t.Fatal("verification mismatch wrote the repository")
	}
	if err := st.SetVerification(context.Background(), first.runID, kept.FinalJSON, kept.VerificationOutcome, done.VerificationDigest); err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, filepath.Join(repo, "greet.py"), "touched\n", 0o644)
	stale := postJSON(t, ts.URL+"/v1/runs/"+first.runID+"/apply", map[string]any{"digest": done.VerificationDigest}, "")
	staleBody := readBody(t, stale)
	if stale.status != http.StatusConflict {
		t.Fatalf("stale %d %s", stale.status, staleBody)
	}
	if got := readRepoFile(t, filepath.Join(repo, "greet.py")); got != "touched\n" {
		t.Fatalf("stale apply wrote %q", got)
	}
	writeRepoFile(t, filepath.Join(repo, "greet.py"), original, 0o644)
	ok := postJSON(t, ts.URL+"/v1/runs/"+first.runID+"/apply", map[string]any{"digest": done.VerificationDigest}, "")
	okBody := readBody(t, ok)
	if ok.status != http.StatusOK {
		t.Fatalf("apply %d %s", ok.status, okBody)
	}
	if !strings.Contains(readRepoFile(t, filepath.Join(repo, "greet.py")), "return \"hello\"\n") {
		t.Fatal("apply did not write the verified implementation")
	}
}

func featureSubmit(repo, script, messageID, goal string, contract any) map[string]any {
	return map[string]any{
		"goal": goal, "content": goal, "repo_path": repo, "provider": "fake",
		"script_path": script, "profile": gateProfile(), "client_message_id": messageID,
		"interaction": "agent", "task_type": "feature", "contract": contract, "max_steps": 8,
	}
}

func featureContractMap() map[string]any {
	return map[string]any{
		"version": "verify.v1",
		"checks": []any{
			checkMap("reg", "regression", "tests.test_regress", "tests/test_regress.py"),
			checkMap("accept", "acceptance", "tests.test_accept", "tests/test_accept.py"),
		},
	}
}

func contractWithPath(path, module string) map[string]any {
	return map[string]any{
		"version": "verify.v1",
		"checks": []any{
			checkMap("reg", "regression", "tests.test_regress", "tests/test_regress.py"),
			checkMap("accept", "acceptance", module, path),
		},
	}
}

func checkMap(name, role, module, path string) map[string]any {
	return map[string]any{
		"name": name, "role": role, "paths": []string{path},
		"argv": []string{"python3", "-m", "unittest", module},
	}
}

func gateProfile() map[string]any {
	return map[string]any{
		"name": "python",
		"argv": []string{"python3", "-m", "unittest", "tests.test_regress"},
	}
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func scriptSteps(steps ...map[string]any) []byte {
	raw, _ := json.Marshal(map[string]any{"steps": steps})
	return raw
}

func patchStep(path, old, new, text string) map[string]any {
	return map[string]any{
		"tool": "apply_patch", "text": text, "input_tokens": 10, "output_tokens": 5,
		"args": map[string]string{"path": path, "old": old, "new": new},
	}
}

func finishStep(text string) map[string]any {
	return map[string]any{"finish": true, "text": text, "input_tokens": 10, "output_tokens": 5}
}

func writeScript(t *testing.T, body []byte) string {
	t.Helper()
	return writeScriptAt(t, t.TempDir(), "script.json", body)
}

func writeScriptAt(t *testing.T, dir, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func copyGateRepo(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join(testutil.RepoRoot(t), "eval", "fixtures", name)
	dst := filepath.Join(t.TempDir(), name)
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}
