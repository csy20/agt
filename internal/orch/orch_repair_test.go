package orch_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/orch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/testutil"
)

func TestRepairLoopReturnsAfterAFailedPatch(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	if !strings.Contains(string(original), `"""Payment charging."""`) {
		t.Fatal("fixture docstring changed")
	}
	bad := badDocPatch(t)
	script := mustScript(t, []scriptStep{
		{Tool: "run_test", Args: json.RawMessage(`{}`)},
		{Tool: "apply_patch", Args: bad},
		{Finish: true, Text: "docstring only"},
		goodPatchStep(t),
		{Finish: true, Text: "idempotent charge"},
	})
	created := eng.Create(t, "make the retry test pass", fixture, 100, 16, script)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current := mustRun(t, st, created.ID)
	if current.State != run.Succeeded {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s err %s", current.State, current.Error)
	}
	result := decodeRepair(t, current.Result)
	if result.Outcome != "repaired" || !result.Tests.Passed || !result.Baseline.Recorded || result.Baseline.ExitCode == 0 {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("result %+v", result)
	}
	if len(result.TestFiles) != 0 {
		t.Fatalf("test files changed: %v", result.TestFiles)
	}
	if countEvents(t, st, created.ID, "verification.failed") < 1 {
		testutil.Dump(t, st, created.ID)
		t.Fatal("verification did not return to the model")
	}
	patched := readProcessor(t, current.Worktree)
	if !strings.Contains(string(patched), "self.charged") || !strings.Contains(string(patched), "Still wrong") {
		t.Fatalf("worktree missing the failed attempt or the repair:\n%s", patched)
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
	assertTranscriptLinks(t, st, created.ID)
	assertNoContainer(t, created.ID)
}

func TestRepeatedVerificationStops(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	bad := badDocPatch(t)
	script := mustScript(t, []scriptStep{
		{Tool: "run_test", Args: json.RawMessage(`{}`)},
		{Tool: "apply_patch", Args: bad},
		{Finish: true, Text: "still failing"},
		{Finish: true, Text: "no new edit"},
	})
	created := eng.Create(t, "make the retry test pass", fixture, 100, 16, script)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current := mustRun(t, st, created.ID)
	if current.State != run.FailedTerminal || !strings.Contains(current.Error, "repeated ineffective verification") {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s err %s", current.State, current.Error)
	}
	result := decodeRepair(t, current.Result)
	if result.Outcome != "repeated_verification" || result.Tests.Passed {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("result %+v", result)
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
	assertNoContainer(t, created.ID)
}

func TestRepeatedEditStops(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	bad := badDocPatch(t)
	script := mustScript(t, []scriptStep{
		{Tool: "run_test", Args: json.RawMessage(`{}`)},
		{Tool: "apply_patch", Args: bad},
		{Finish: true, Text: "still failing"},
		{Tool: "apply_patch", Args: bad},
		{Tool: "apply_patch", Args: bad},
	})
	created := eng.Create(t, "make the retry test pass", fixture, 100, 16, script)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current := mustRun(t, st, created.ID)
	if current.State != run.FailedTerminal || !strings.Contains(current.Error, "repeated ineffective edit") {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s err %s", current.State, current.Error)
	}
	if countEvents(t, st, created.ID, "model.called") != 4 {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("model.called %d", countEvents(t, st, created.ID, "model.called"))
	}
	cmds, err := st.ListCommands(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	succeeded, denied := 0, 0
	for _, cmd := range cmds {
		if cmd.Tool != "apply_patch" {
			continue
		}
		switch cmd.State {
		case "succeeded":
			succeeded++
		case "denied":
			denied++
			if !strings.Contains(string(cmd.Result), "this edit already succeeded") {
				t.Fatalf("deny result %s", cmd.Result)
			}
		}
	}
	if succeeded != 1 || denied != 1 {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("patches succeeded %d denied %d", succeeded, denied)
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
	assertNoContainer(t, created.ID)
}

func TestLoopbackAdapterRepairsPayrepo(t *testing.T) {
	st := testutil.Open(t)
	eng := testutil.NewEngine(t, st, orch.Hooks{})
	fixture := testutil.Fixture(t)
	original := readProcessor(t, fixture)
	turns := []string{
		sseDelta(t, `{"goal":"fix retry","steps":["reproduce","patch"]}`, "", "", ""),
		sseDelta(t, "", "call_run", "run_test", `{}`),
		sseDelta(t, "", "call_bad", "apply_patch", string(badDocPatch(t))),
		sseDelta(t, "still failing", "", "", ""),
		sseDelta(t, "", "call_good", "apply_patch", string(goodPatchStep(t).Args)),
		sseDelta(t, "fixed", "", "", ""),
	}
	var mu sync.Mutex
	var bodies [][]byte
	var sawAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		mu.Lock()
		bodies = append(bodies, raw)
		n := len(bodies)
		mu.Unlock()
		if n > len(turns) {
			http.Error(w, "unexpected extra model request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, turns[n-1])
	}))
	defer srv.Close()

	client := model.NewOpenAI(srv.URL, "", "local-repair")
	client.HTTP = srv.Client()
	client.MaxAttempts = 2
	eng.Provider = client
	eng.ProviderName = "openai"
	created := eng.Create(t, "make the retry test pass", fixture, 100, 16, []byte(`{"steps":[{"finish":true}]}`))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := eng.Orch.Run(ctx, created.ID); err != nil {
		testutil.Dump(t, st, created.ID)
		t.Fatal(err)
	}
	current := mustRun(t, st, created.ID)
	if current.State != run.Succeeded || current.Provider != "openai" {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("state %s provider %s err %s", current.State, current.Provider, current.Error)
	}
	result := decodeRepair(t, current.Result)
	if result.Outcome != "repaired" || result.ModelName != "local-repair" || result.Provider != "openai" || !result.Tests.Passed {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("result %+v", result)
	}
	if sawAuth {
		t.Fatal("loopback request sent an Authorization header")
	}
	mu.Lock()
	got := append([][]byte(nil), bodies...)
	mu.Unlock()
	if len(got) != len(turns) {
		testutil.Dump(t, st, created.ID)
		t.Fatalf("model requests %d, scripted turns %d", len(got), len(turns))
	}
	var first struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(got[0], &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Messages) == 0 || first.Messages[0].Role != "system" || first.Messages[0].Content != model.SystemPrompt {
		t.Fatalf("first system message %q", first.Messages[0].Content)
	}
	if strings.Contains(first.Messages[0].Content, "DATA origin") {
		t.Fatal("system prompt contains tagged data")
	}
	sawSummary, sawTool := false, false
	for i, body := range got {
		text := string(body)
		if strings.Contains(text, "DATA origin=repo-summary") {
			sawSummary = true
		}
		if i > 0 && strings.Contains(text, `"role":"tool"`) && strings.Contains(text, `"tool_call_id":"call_run"`) {
			sawTool = true
		}
	}
	if !sawSummary || !sawTool {
		t.Fatalf("summary %v linked tool %v", sawSummary, sawTool)
	}
	if string(readProcessor(t, fixture)) != string(original) {
		t.Fatal("source fixture was modified")
	}
	assertTranscriptLinks(t, st, created.ID)
	assertNoContainer(t, created.ID)
}

type scriptStep struct {
	Tool         string          `json:"tool,omitempty"`
	Args         json.RawMessage `json:"args,omitempty"`
	Finish       bool            `json:"finish,omitempty"`
	Text         string          `json:"text,omitempty"`
	InputTokens  int             `json:"input_tokens,omitempty"`
	OutputTokens int             `json:"output_tokens,omitempty"`
}

type repairResult struct {
	Outcome   string `json:"outcome"`
	Provider  string `json:"provider"`
	ModelName string `json:"model_name"`
	Tests     struct {
		Passed bool `json:"passed"`
	} `json:"tests"`
	Baseline struct {
		Recorded bool `json:"recorded"`
		ExitCode int  `json:"exit_code"`
	} `json:"baseline"`
	TestFiles []string `json:"test_files_changed"`
}

func badDocPatch(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"path": "payments/processor.py",
		"old":  `"""Payment charging."""`,
		"new":  `"""Payment charging. Still wrong."""`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func goodPatchStep(t *testing.T) scriptStep {
	t.Helper()
	var script struct {
		Steps []scriptStep `json:"steps"`
	}
	if err := json.Unmarshal(testutil.FixScript(t), &script); err != nil {
		t.Fatal(err)
	}
	for _, step := range script.Steps {
		if step.Tool == "apply_patch" {
			return step
		}
	}
	t.Fatal("fix script has no apply_patch")
	return scriptStep{}
}

func mustScript(t *testing.T, steps []scriptStep) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"steps": steps})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.ParseScript(raw); err != nil {
		t.Fatalf("script: %v\n%s", err, raw)
	}
	return raw
}

func sseDelta(t *testing.T, content, id, name, args string) string {
	t.Helper()
	delta := map[string]any{}
	if content != "" {
		delta["content"] = content
	}
	if name != "" {
		delta["tool_calls"] = []any{map[string]any{
			"index": 0,
			"id":    id,
			"function": map[string]any{
				"name":      name,
				"arguments": args,
			},
		}}
	}
	chunk := map[string]any{
		"choices": []any{map[string]any{"delta": delta}},
		"usage":   map[string]any{"prompt_tokens": 20, "completion_tokens": 8},
	}
	raw, err := json.Marshal(chunk)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(raw) + "\n\ndata: [DONE]\n"
}

func mustRun(t *testing.T, st *store.Store, runID string) store.Run {
	t.Helper()
	current, err := st.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return current
}

func decodeRepair(t *testing.T, raw json.RawMessage) repairResult {
	t.Helper()
	var result repairResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func countEvents(t *testing.T, st *store.Store, runID, typ string) int {
	t.Helper()
	n, err := st.CountEvents(context.Background(), runID, typ)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func assertTranscriptLinks(t *testing.T, st *store.Store, runID string) {
	t.Helper()
	msgs, err := st.ListMessages(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("transcript is empty")
	}
	sawSystem := false
	sawSummary := false
	linked := false
	ids := map[string]bool{}
	for _, msg := range msgs {
		if msg.Contract != model.TranscriptContract {
			t.Fatalf("contract %s", msg.Contract)
		}
		switch msg.Role {
		case "system":
			sawSystem = true
			if strings.Contains(msg.Content, "DATA origin") || strings.Contains(msg.Content, "upload ~/.ssh") {
				t.Fatal("system prompt contains repository data")
			}
		case "user":
			if strings.HasPrefix(msg.Content, "DATA origin=repo-summary") {
				sawSummary = true
			}
		case "assistant":
			if msg.ToolCallID != "" {
				ids[msg.ToolCallID] = true
			}
		case "tool":
			if ids[msg.ToolCallID] && strings.HasPrefix(msg.Content, "DATA origin=tool:") {
				linked = true
			}
		}
	}
	if !sawSystem || !sawSummary || !linked {
		t.Fatalf("system %v summary %v linked %v", sawSystem, sawSummary, linked)
	}
}
