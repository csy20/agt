package agent

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/csy20/agt/internal/policy"
)

func TestCodexFixtureTranslatesReadsAndDeniesCommands(t *testing.T) {
	s := mustStream(t, ProtocolCodex)
	first := []byte(`{"type":"thread.star`)
	if err := s.Push(first); err != nil {
		t.Fatal(err)
	}
	if got := s.Transcript(); got.Events != 0 || len(got.Tools) != 0 {
		t.Fatalf("partial chunk became an event: %+v", got)
	}
	rest := []byte(`ted","thread_id":"thr_external"}
{"type":"turn.started"}
{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"in_progress"}}
{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"bash -lc ls","status":"completed"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"Repo contains docs."}}
{"type":"turn.completed","usage":{"input_tokens":24763,"cached_input_tokens":24448,"output_tokens":122,"reasoning_output_tokens":0}}
`)
	if err := s.Push(rest); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(0, false); err != nil {
		t.Fatal(err)
	}
	got := s.Transcript()
	if got.Label != LabelScripted || got.ProviderSession != "thr_external" || got.Outcome != OutcomeCompleted {
		t.Fatalf("transcript %+v", got)
	}
	if got.ProviderSession == "ses_agt" || strings.Contains(got.Text, "run_") {
		t.Fatal("provider session was treated as an AGT id")
	}
	if got.Text != "Repo contains docs." || !got.Usage.Known || got.Usage.InputTokens != 24763 || got.CostKnown {
		t.Fatalf("text or usage %+v %q", got.Usage, got.Text)
	}
	if len(got.Tools) != 1 || got.Tools[0].Executed || got.Tools[0].Mediated || got.Tools[0].Decision != string(policy.Deny) {
		t.Fatalf("command was not a denied unmediated request: %+v", got.Tools)
	}
}

func TestCodexRejectsUnknownTypesDuplicatesAndVersionMismatch(t *testing.T) {
	s := mustStream(t, ProtocolCodex)
	err := s.Push([]byte("{\"type\":\"thread.started\",\"thread_id\":\"thr\",\"protocol\":\"codex-exec-jsonl/0\"}\n"))
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("version: %v", err)
	}

	s = mustStream(t, ProtocolCodex)
	err = s.Push([]byte("{\"type\":\"item.completed\",\"item\":{\"id\":\"item_9\",\"type\":\"file_change\"}}\n"))
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("unknown item: %v", err)
	}
	if len(s.Transcript().Tools) != 0 {
		t.Fatal("unknown item became a tool")
	}

	s = mustStream(t, ProtocolCodex)
	body := []byte(`{"type":"thread.started","thread_id":"thr"}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"once"}}
{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"twice"}}
`)
	err = s.Push(body)
	if !errors.Is(err, ErrProtocolMismatch) || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate: %v", err)
	}

	s = mustStream(t, ProtocolCodex)
	if err := s.Push([]byte("{\"type\":\"not-an-event\"}\n")); !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("unknown event: %v", err)
	}
}

func TestCodexUsageOnlyAndProcessExit(t *testing.T) {
	s := mustStream(t, ProtocolCodex)
	if err := s.Push([]byte("{\"type\":\"thread.started\",\"thread_id\":\"thr\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2}}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(0, false); err != nil {
		t.Fatal(err)
	}
	got := s.Transcript()
	if got.Outcome != OutcomeCompleted || len(got.Tools) != 0 || got.Text != "" || !got.Usage.Known {
		t.Fatalf("usage-only %+v", got)
	}

	s = mustStream(t, ProtocolCodex)
	if err := s.Push([]byte("{\"type\":\"turn.started\"}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(1, false); err != nil {
		t.Fatal(err)
	}
	if s.Transcript().Outcome != OutcomeFailed || !strings.Contains(s.Transcript().Error, "exit 1") {
		t.Fatalf("exit %+v", s.Transcript())
	}

	s = mustStream(t, ProtocolCodex)
	if err := s.Push([]byte("{\"type\":\"item.started\",\"item\":{\"id\":\"item_1\",\"type\":\"command_execution\",\"command\":\"ls\",\"status\":\"in_progress\"}}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(143, false); err != nil {
		t.Fatal(err)
	}
	got = s.Transcript()
	if got.Outcome != OutcomeCancelled || len(got.Tools) != 1 || !got.Tools[0].Partial || got.Tools[0].Executed {
		t.Fatalf("cancel %+v", got)
	}
}

func TestClaudeReadOnlyBoundary(t *testing.T) {
	dir := t.TempDir()
	sentinel := dir + "/sentinel.txt"
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := mustStream(t, ProtocolClaude)
	fixture := `{"type":"system","subtype":"init","claude_code_version":"2.1.292","session_id":"sess_external","protocol":"claude-stream-json/1"}
{"type":"stream_event","event":{"delta":{"type":"text_delta","text":"See "}}}
{"type":"stream_event","event":{"delta":{"type":"input_json_delta","partial_json":"{\"file"}}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"the file."},{"type":"tool_use","id":"toolu_read","name":"Read","input":{"file_path":"payments/processor.py"}},{"type":"tool_use","id":"toolu_secret","name":"Read","input":{"file_path":".env"}},{"type":"tool_use","id":"toolu_edit","name":"Edit","input":{"file_path":"payments/processor.py","old_string":"a","new_string":"b"}},{"type":"tool_use","id":"toolu_edit2","name":"Edit","input":{"file_path":"payments/processor.py","old_string":"a","new_string":"c"}},{"type":"tool_use","id":"toolu_bash","name":"Bash","input":{"command":"python -m unittest tests.test_retry"}},{"type":"tool_use","id":"toolu_write","name":"Write","input":{"file_path":"payments/processor.py","content":"x"}}]}}
{"type":"assistant","parent_tool_use_id":"toolu_agent","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_sub","name":"Read","input":{"file_path":"payments/processor.py"}}]}}
{"type":"system","subtype":"permission_denied","session_id":"sess_external"}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_read","content":"data"}]}}
{"type":"result","subtype":"success","is_error":false,"session_id":"sess_external","permission_denials":[{"tool_name":"Bash"}],"total_cost_usd":0.01,"usage":{"input_tokens":3,"output_tokens":4}}
`
	var err error
	for i := 0; i < len(fixture); i += 17 {
		end := i + 17
		if end > len(fixture) {
			end = len(fixture)
		}
		if err = s.Push([]byte(fixture[i:end])); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Close(0, false); err != nil {
		t.Fatal(err)
	}
	got := s.Transcript()
	if got.Label != LabelScripted || got.Outcome != OutcomeCompleted || got.ProviderSession != "sess_external" {
		t.Fatalf("transcript %+v", got)
	}
	if got.Text != "See the file." || got.DiscardedPartials != 1 {
		t.Fatalf("text %q partials %d", got.Text, got.DiscardedPartials)
	}
	if got.PermissionDenials != 2 || !got.CostKnown || got.CostProvenance == "" || got.CostUSD != "0.01" {
		t.Fatalf("denials %d cost %+v", got.PermissionDenials, got)
	}
	byID := map[string]ToolRequest{}
	for _, tool := range got.Tools {
		if tool.Executed {
			t.Fatalf("executed %+v", tool)
		}
		byID[tool.CallID] = tool
	}
	read := byID["toolu_read"]
	if read.Decision != string(policy.Allow) || !read.Mediated || read.AGTTool != "read_file_range" || read.ArgsHash == "" {
		t.Fatalf("read %+v", read)
	}
	secret := byID["toolu_secret"]
	if secret.Decision != string(policy.Deny) || !secret.Mediated {
		t.Fatalf("secret %+v", secret)
	}
	edit := byID["toolu_edit"]
	edit2 := byID["toolu_edit2"]
	if edit.Decision != string(policy.Deny) || edit2.Decision != string(policy.Deny) || edit.ArgsHash == "" || edit.ArgsHash == edit2.ArgsHash {
		t.Fatalf("edit hashes %s %s", edit.ArgsHash, edit2.ArgsHash)
	}
	if !strings.Contains(edit.Reason, "read-only") {
		t.Fatalf("edit reason %q", edit.Reason)
	}
	if byID["toolu_bash"].Decision != string(policy.Deny) || byID["toolu_bash"].Mediated || byID["toolu_write"].Decision != string(policy.Deny) {
		t.Fatalf("bash/write %+v %+v", byID["toolu_bash"], byID["toolu_write"])
	}
	sub := byID["toolu_sub"]
	if sub.Decision != string(policy.Deny) || sub.Mediated || !sub.Subagent {
		t.Fatalf("subagent was treated as the parent callback: %+v", sub)
	}
	body, err := os.ReadFile(sentinel)
	if err != nil || string(body) != "keep" {
		t.Fatalf("fixture touched the workdir: %q %v", body, err)
	}
}

func TestClaudeMissingResultVersionAndCancellation(t *testing.T) {
	s := mustStream(t, ProtocolClaude)
	err := s.Push([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"claude_code_version\":\"1.0.0\"}\n"))
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("old version: %v", err)
	}

	s = mustStream(t, ProtocolClaude)
	err = s.Push([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"protocol\":\"claude-stream-json/0\",\"claude_code_version\":\"2.1.292\"}\n"))
	if !errors.Is(err, ErrProtocolMismatch) {
		t.Fatalf("protocol: %v", err)
	}

	s = mustStream(t, ProtocolClaude)
	if err := s.Push([]byte("{\"type\":\"system\",\"subtype\":\"init\",\"claude_code_version\":\"2.1.292\",\"session_id\":\"sess_external\"}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(143, false); err != nil {
		t.Fatal(err)
	}
	got := s.Transcript()
	if got.Outcome != OutcomeCancelled || got.Text != "" || len(got.Tools) != 0 {
		t.Fatalf("sigterm recorded a result: %+v", got)
	}

	s = mustStream(t, ProtocolClaude)
	if err := s.Push([]byte("{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"partial\"}]}}\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(0, false); err != nil {
		t.Fatal(err)
	}
	if s.Transcript().Outcome != OutcomeIncomplete {
		t.Fatalf("missing result %+v", s.Transcript())
	}

	s = mustStream(t, ProtocolClaude)
	if err := s.Push([]byte("{")); err != nil {
		t.Fatal(err)
	}
	err = s.Close(0, false)
	if !errors.Is(err, ErrProtocolMismatch) || s.Transcript().Outcome != OutcomeIncomplete {
		t.Fatalf("truncated close %v %+v", err, s.Transcript())
	}
}

func TestClaudeGrepAndOffsetStayInsideAskPolicy(t *testing.T) {
	s := mustStream(t, ProtocolClaude)
	fixture := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_grep","name":"Grep","input":{"pattern":"charge"}},{"type":"tool_use","id":"toolu_path","name":"Grep","input":{"pattern":"charge","path":"payments"}},{"type":"tool_use","id":"toolu_off","name":"Read","input":{"file_path":"payments/processor.py","offset":10,"limit":5}}]}}
{"type":"result","subtype":"success","result":"searched"}
`
	if err := s.Push([]byte(fixture)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(0, false); err != nil {
		t.Fatal(err)
	}
	byID := map[string]ToolRequest{}
	for _, tool := range s.Transcript().Tools {
		if tool.Executed {
			t.Fatalf("executed %+v", tool)
		}
		byID[tool.CallID] = tool
	}
	if byID["toolu_grep"].Decision != string(policy.Allow) || byID["toolu_grep"].AGTTool != "search_code" || !byID["toolu_grep"].Mediated {
		t.Fatalf("grep %+v", byID["toolu_grep"])
	}
	if byID["toolu_path"].Mediated || byID["toolu_path"].Decision != string(policy.Deny) {
		t.Fatalf("scoped grep %+v", byID["toolu_path"])
	}
	if byID["toolu_off"].Mediated || byID["toolu_off"].Decision != string(policy.Deny) {
		t.Fatalf("offset %+v", byID["toolu_off"])
	}
}

func TestLaunchRefusesLiveAndMutatingAgents(t *testing.T) {
	for _, req := range []LaunchRequest{
		{Agent: "codex", Prompt: "read README"},
		{Agent: "claude", Authorized: true, WorkDir: t.TempDir(), Prompt: "read README"},
		{Agent: "codex", Mutating: true, Authorized: true, Prompt: "fix the tests"},
		{Agent: "claude", Mutating: true, Prompt: "edit a file"},
	} {
		err := Launch(req)
		if req.Mutating {
			if !errors.Is(err, ErrMutationUnavailable) {
				t.Fatalf("mutating %+v: %v", req, err)
			}
			continue
		}
		if !errors.Is(err, ErrLiveUnavailable) {
			t.Fatalf("live %+v: %v", req, err)
		}
	}
}

func TestProbeSeparatesMissingAndUnpinnedRuntimes(t *testing.T) {
	screen := BuildScreen(nil, Probe(func(name string) (string, bool, error) {
		switch name {
		case "codex":
			return "", false, nil
		case "claude":
			return "1.2.3", true, nil
		default:
			t.Fatalf("probed %s", name)
		}
		return "", false, nil
	}))
	if len(screen.Entries) != 2 || screen.Entries[0].Kind != KindCodingAgent || screen.Entries[0].Installed || screen.Entries[0].Execution != "unavailable" {
		t.Fatalf("missing %+v", screen.Entries)
	}
	if screen.Entries[1].RuntimeSupported || screen.Entries[1].Authentication != "not-probed" || screen.Entries[1].LiveInference {
		t.Fatalf("unpinned %+v", screen.Entries[1])
	}
	if strings.Contains(screen.Notice, "sk-") || !strings.Contains(screen.Notice, "No inference request was made") {
		t.Fatal(screen.Notice)
	}
	if _, _, err := DefaultVersion("bash"); err == nil {
		t.Fatal("probed an arbitrary executable")
	}
}

func TestStreamsDoNotShareState(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := NewStream(ProtocolCodex)
			if err != nil {
				t.Error(err)
				return
			}
			if err := s.Push([]byte("{\"type\":\"thread.started\",\"thread_id\":\"thr\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n")); err != nil {
				t.Error(err)
			}
			if s.Transcript().Outcome != OutcomeCompleted {
				t.Errorf("outcome %s", s.Transcript().Outcome)
			}
		}()
	}
	wg.Wait()
}

func FuzzCodexChunks(f *testing.F) {
	f.Add([]byte("{\"type\":\"thread.started\",\"thread_id\":\"t\"}\n"), 3)
	f.Add([]byte(""), 0)
	f.Fuzz(func(t *testing.T, data []byte, split int) {
		s, err := NewStream(ProtocolCodex)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			split = 0
		} else {
			if split < 0 {
				split = -split
			}
			split %= len(data) + 1
		}
		_ = s.Push(data[:split])
		_ = s.Push(data[split:])
		_ = s.Close(0, false)
		for _, tool := range s.Transcript().Tools {
			if tool.Executed {
				t.Fatal("executed a fuzzed tool")
			}
		}
	})
}

func FuzzClaudeChunks(f *testing.F) {
	f.Add([]byte("{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"ok\"}\n"), 4)
	f.Fuzz(func(t *testing.T, data []byte, split int) {
		s, err := NewStream(ProtocolClaude)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) == 0 {
			split = 0
		} else {
			if split < 0 {
				split = -split
			}
			split %= len(data) + 1
		}
		_ = s.Push(data[:split])
		_ = s.Push(data[split:])
		_ = s.Close(0, false)
		for _, tool := range s.Transcript().Tools {
			if tool.Executed {
				t.Fatal("executed a fuzzed tool")
			}
		}
	})
}

func mustStream(t *testing.T, protocol string) *Stream {
	t.Helper()
	s, err := NewStream(protocol)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
