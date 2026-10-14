package orch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/redact"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/telemetry"
	"github.com/csy20/agt/internal/tools"
	"github.com/csy20/agt/internal/usage"
	"github.com/csy20/agt/internal/workspace"
)

var (
	// ErrHalt means the run is waiting on a human or a budget stop.
	ErrHalt = errors.New("halted")
	// ErrInterrupt leaves a pending command so another worker can resume it.
	ErrInterrupt = errors.New("worker interrupted")
)

type Hooks struct {
	// BeforeResult runs after a tool side effect and before the result is recorded.
	BeforeResult func(commandID, tool string) error
}

type Options struct {
	Store        *store.Store
	DataDir      string
	Prices       usage.Prices
	ModelFor     func(store.Run) (model.Provider, error)
	NewWorkspace func(store.Run) (workspace.Handle, error)
	Hooks        Hooks
	Tel          telemetry.Emitter
}

type Orchestrator struct {
	store        *store.Store
	dataDir      string
	prices       usage.Prices
	modelFor     func(store.Run) (model.Provider, error)
	newWorkspace func(store.Run) (workspace.Handle, error)
	hooks        Hooks
	tel          telemetry.Emitter

	mu   sync.Mutex
	open map[string]workspace.Handle
	mark map[string]int64
}

func New(opts Options) *Orchestrator {
	tel := opts.Tel
	if tel == nil {
		tel = telemetry.Discard{}
	}
	return &Orchestrator{
		store:        opts.Store,
		dataDir:      opts.DataDir,
		prices:       opts.Prices,
		modelFor:     opts.ModelFor,
		newWorkspace: opts.NewWorkspace,
		hooks:        opts.Hooks,
		tel:          tel,
		open:         map[string]workspace.Handle{},
		mark:         map[string]int64{},
	}
}

// Run advances one run until it stops, succeeds, or the worker is interrupted.
func (o *Orchestrator) Run(ctx context.Context, runID string) error {
	for i := 0; i < 64; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		current, err := o.store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		o.flush(ctx, current)
		if run.IsTerminal(current.State) {
			_ = o.close(ctx, runID)
			return nil
		}
		if !current.Deadline.IsZero() && time.Now().After(current.Deadline) && current.State != run.PausedBudget && current.State != run.Paused {
			if err := o.close(ctx, runID); err != nil {
				return o.fail(ctx, current, err)
			}
			if _, err := o.store.Transition(ctx, runID, current.StateVersion, run.PausedBudget, "deadline"); err != nil {
				return err
			}
			return nil
		}
		if err := o.executeOpen(ctx, current); err != nil {
			if errors.Is(err, ErrHalt) {
				return nil
			}
			if errors.Is(err, ErrInterrupt) {
				return err
			}
			return o.fail(ctx, current, err)
		}
		current, err = o.store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		if run.IsTerminal(current.State) {
			_ = o.close(ctx, runID)
			return nil
		}
		switch current.State {
		case run.AwaitingApproval, run.Paused, run.PausedBudget:
			return nil
		case run.Created:
			_, err = o.store.Transition(ctx, runID, current.StateVersion, run.Contextualising, "orchestrator")
		case run.Contextualising:
			err = o.seedContext(ctx, current)
		case run.Planning, run.Observing:
			err = o.modelTurn(ctx, current)
		case run.Verifying:
			err = o.verify(ctx, current)
		case run.Executing:
			_, openErr := o.store.OpenCommand(ctx, runID)
			if errors.Is(openErr, store.ErrNotFound) {
				_, err = o.store.Transition(ctx, runID, current.StateVersion, run.Observing, "orchestrator")
			}
		case run.Committing:
			// Destroy the sandbox before the terminal state is visible, so a
			// client that disconnects on success cannot interrupt cleanup.
			if err := o.close(ctx, runID); err != nil {
				return o.fail(ctx, current, err)
			}
			if _, err = o.store.Transition(ctx, runID, current.StateVersion, run.Succeeded, "orchestrator"); err != nil {
				return o.fail(ctx, current, err)
			}
			_, _ = o.store.AppendEvent(ctx, runID, "run.succeeded", "system", "orchestrator", map[string]any{
				"run_id": runID,
			})
			return nil
		default:
			err = fmt.Errorf("no handler for state %s", current.State)
		}
		if err != nil {
			if errors.Is(err, ErrInterrupt) {
				return err
			}
			return o.fail(ctx, current, err)
		}
	}
	current, _ := o.store.GetRun(ctx, runID)
	return o.fail(ctx, current, fmt.Errorf("orchestrator loop exceeded"))
}

func (o *Orchestrator) modelTurn(ctx context.Context, current store.Run) error {
	if current.MaxSteps >= 0 && current.StepCount >= current.MaxSteps {
		_, err := o.store.Transition(ctx, current.ID, current.StateVersion, run.PausedBudget, "budget")
		return err
	}
	provider, err := o.modelFor(current)
	if err != nil {
		return err
	}
	completed, err := o.store.CountEvents(ctx, current.ID, "model.called")
	if err != nil {
		return err
	}
	obs, err := o.observations(ctx, current.ID)
	if err != nil {
		return err
	}
	turns, err := o.requestTurns(ctx, current)
	if err != nil {
		return err
	}
	resp, err := provider.Complete(ctx, model.Request{
		Goal:           current.Goal,
		System:         model.SystemPrompt,
		Plan:           current.Plan,
		Observations:   obs,
		Transcript:     turns,
		CompletedCalls: completed,
	})
	if err != nil {
		return err
	}
	cost := usage.CostMicros(int64(resp.InputTokens), o.prices.InputPerMillion) +
		usage.CostMicros(int64(resp.OutputTokens), o.prices.OutputPerMillion)
	intent := store.Intent{
		Plan:         resp.Plan,
		Text:         resp.Text,
		ToolCallID:   resp.ToolCallID,
		ModelName:    resp.ModelName,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		CostMicros:   cost,
		Model:        current.Provider,
	}
	if current.SpentMicros+cost > usage.CentsToMicros(int64(current.BudgetCents)) {
		intent.Kind = "budget"
	} else if resp.Finish {
		intent.Kind = "finish"
	} else if resp.Tool == "" && len(resp.Plan) > 0 {
		intent.Kind = "plan"
	} else if resp.Tool == "" {
		return fmt.Errorf("model returned no action")
	} else {
		decision, err := policy.Decide(resp.Tool, resp.Args)
		if err != nil {
			return err
		}
		if isEdit(resp.Tool) {
			repeat, err := o.repeatedEdit(ctx, current.ID, resp.Tool, decision.ArgsHash)
			if err != nil {
				return err
			}
			if repeat == editStop {
				return fmt.Errorf("repeated ineffective edit")
			}
			if repeat == editDeny {
				decision.Decision = policy.Deny
				decision.Reason = "this edit already succeeded"
			}
		}
		intent.Kind = "tool"
		intent.Tool = resp.Tool
		intent.Canonical = decision.Canonical
		intent.ArgsHash = decision.ArgsHash
		intent.Decision = string(decision.Decision)
		intent.Reason = decision.Reason
		intent.CommandID = id.New("cmd_")
		if intent.ToolCallID == "" {
			intent.ToolCallID = "call_" + intent.CommandID
		}
	}
	_, err = o.store.CommitModelIntent(ctx, current.ID, current.StateVersion, intent)
	return err
}

func (o *Orchestrator) executeOpen(ctx context.Context, current store.Run) error {
	cmd, err := o.store.OpenCommand(ctx, current.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if cmd.State == "awaiting_approval" {
		return ErrHalt
	}
	output, code, err := o.perform(ctx, current, cmd)
	if errors.Is(err, ErrInterrupt) {
		return err
	}
	if err == nil && o.hooks.BeforeResult != nil {
		if herr := o.hooks.BeforeResult(cmd.CommandID, cmd.Tool); herr != nil {
			return herr
		}
	}
	if err != nil {
		_ = o.store.FinishCommand(ctx, cmd.CommandID, "failed", map[string]any{
			"exit_code": code, "output": err.Error(),
		})
		return err
	}
	if err := o.store.FinishCommand(ctx, cmd.CommandID, "succeeded", map[string]any{
		"exit_code": code, "output": output,
	}); err != nil {
		return err
	}
	if cmd.Tool == "run_test" {
		if err := o.noteBaseline(ctx, current.ID, output, code); err != nil {
			return err
		}
	}
	_, _ = o.store.AppendEvent(ctx, current.ID, "command.finished", "tool", cmd.Tool, map[string]any{
		"command_id": cmd.CommandID, "tool": cmd.Tool, "exit_code": code,
		"output_excerpt": modelText(output, 500), "args_hash": cmd.ArgsHash,
	})
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	if fresh.State == run.Executing {
		_, err = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.Observing, "orchestrator")
	}
	return err
}

func (o *Orchestrator) perform(ctx context.Context, current store.Run, cmd store.Command) (string, int, error) {
	switch cmd.Tool {
	case "git_push", "pr_open":
		return "", 2, fmt.Errorf("unsupported tool %s: remote mutations are disabled and no process was spawned", cmd.Tool)
	}
	ws, err := o.workspace(ctx, current)
	if err != nil {
		return "", 0, err
	}
	switch cmd.Tool {
	case "search_code":
		var args tools.SearchArgs
		if err := json.Unmarshal(cmd.Canonical, &args); err != nil {
			return err.Error(), 2, nil
		}
		return ws.Search(ctx, args.Query)
	case "read_file_range":
		var args tools.ReadArgs
		if err := json.Unmarshal(cmd.Canonical, &args); err != nil {
			return err.Error(), 2, nil
		}
		return ws.Read(ctx, args.Path, args.Start, args.End)
	case "git_diff":
		return ws.Diff(ctx)
	case "run_test":
		return ws.Test(ctx)
	case "apply_patch":
		var args tools.PatchArgs
		if err := json.Unmarshal(cmd.Canonical, &args); err != nil {
			return err.Error(), 2, nil
		}
		return ws.Patch(ctx, args.Path, args.Old, args.New)
	case "write_file":
		var write tools.WriteArgs
		if err := json.Unmarshal(cmd.Canonical, &write); err != nil {
			return err.Error(), 2, nil
		}
		return ws.Write(ctx, write)
	case "delete_file":
		var del tools.DeleteArgs
		if err := json.Unmarshal(cmd.Canonical, &del); err != nil {
			return err.Error(), 2, nil
		}
		return ws.Delete(ctx, del)
	default:
		return "unsupported tool", 2, nil
	}
}

const maxVerificationReturns = 2

func (o *Orchestrator) verify(ctx context.Context, current store.Run) error {
	ws, err := o.workspace(ctx, current)
	if err != nil {
		return o.finishUnverified(ctx, current, "verification_unavailable", "workspace: "+err.Error())
	}
	output, code, err := ws.Test(ctx)
	if err != nil {
		return o.finishUnverified(ctx, current, "verification_unavailable", err.Error())
	}
	diff, diffCode, diffErr := ws.Diff(ctx)
	if diffErr != nil {
		return o.finishUnverified(ctx, current, "verification_unavailable", diffErr.Error())
	}
	if diffCode != 0 {
		return o.finishUnverified(ctx, current, "verification_unavailable", fmt.Sprintf("git_diff exited %d", diffCode))
	}
	decision, err := policy.Decide("run_test", []byte(`{}`))
	if err != nil {
		return err
	}
	commandID := id.New("cmd_")
	if err := o.store.RecordSystemCommand(ctx, current.ID, commandID, "run_test", decision.ArgsHash, decision.Canonical, output, code); err != nil {
		return err
	}
	if err := o.noteBaseline(ctx, current.ID, output, code); err != nil {
		return err
	}
	if code != 0 {
		return o.verificationFailed(ctx, current, output, code, diff)
	}
	return o.verificationPassed(ctx, current, output, code, diff)
}

func (o *Orchestrator) verificationFailed(ctx context.Context, current store.Run, output string, code int, diff string) error {
	fp := fingerprint(output)
	attempts, seen, err := o.verificationHistory(ctx, current.ID, fp)
	if err != nil {
		return err
	}
	reason := "tests failed"
	outcome := "verification_failed"
	terminal := false
	switch {
	case seen:
		terminal = true
		reason = "repeated ineffective verification"
		outcome = "repeated_verification"
	case attempts >= maxVerificationReturns:
		terminal = true
	case current.MaxSteps >= 0 && current.StepCount >= current.MaxSteps:
		terminal = true
		reason = "verification failed and the step budget is exhausted"
		outcome = "verification_failed_budget"
	case current.SpentMicros > usage.CentsToMicros(int64(current.BudgetCents)):
		terminal = true
		reason = "verification failed and the cost budget is exhausted"
		outcome = "verification_failed_budget"
	}
	if _, err := o.store.AppendEvent(ctx, current.ID, "verification.failed", "system", "verifier", map[string]any{
		"exit_code": code, "fingerprint": fp, "attempt": attempts + 1, "outcome": outcome,
	}); err != nil {
		return err
	}
	if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{
		Role:    "user",
		Content: "DATA origin=verification\nexit=" + strconv.Itoa(code) + "\n" + modelText(output, 2000),
	}); err != nil {
		return err
	}
	if !terminal {
		fresh, err := o.store.GetRun(ctx, current.ID)
		if err != nil {
			return err
		}
		_, err = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.Observing, "verifier")
		return err
	}
	return o.finishVerified(ctx, current, outcome, reason, diff, output, code, false)
}

func (o *Orchestrator) verificationPassed(ctx context.Context, current store.Run, output string, code int, diff string) error {
	base, found, err := o.baselineRecord(ctx, current.ID)
	if err != nil {
		return err
	}
	mutated, err := o.hasMutation(ctx, current.ID)
	if err != nil {
		return err
	}
	testFiles, err := o.testFilesChanged(ctx, current.ID)
	if err != nil {
		return err
	}
	outcome := "repaired"
	summary := "verification passed"
	fail := false
	switch {
	case !found:
		outcome = "baseline_missing"
		summary = "tests passed without a recorded baseline failure"
		fail = true
	case base == 0:
		outcome = "baseline_already_passed"
		summary = "tests passed before any edit; this is not a verified repair"
		fail = true
	case !mutated:
		outcome = "passed_without_edit"
		summary = "verification passed without a code change after a failing baseline"
		fail = true
	case len(testFiles) > 0:
		outcome = "passed_after_test_edit"
		summary = "verification passed after a test file changed: " + strings.Join(testFiles, ", ")
	}
	if fail {
		return o.finishVerified(ctx, current, outcome, summary, diff, output, code, false)
	}
	return o.finishVerified(ctx, current, outcome, summary, diff, output, code, true)
}

func (o *Orchestrator) workspace(ctx context.Context, current store.Run) (workspace.Handle, error) {
	o.mu.Lock()
	if ws, ok := o.open[current.ID]; ok {
		o.mu.Unlock()
		return ws, nil
	}
	o.mu.Unlock()

	fresh := current
	if fresh.Worktree == "" {
		work, _, err := workspace.Materialize(current.RepoPath, o.dataDir, current.ID)
		if err != nil {
			return nil, err
		}
		if err := o.store.SetWorktree(ctx, current.ID, work); err != nil {
			return nil, err
		}
		var getErr error
		fresh, getErr = o.store.GetRun(ctx, current.ID)
		if getErr != nil {
			return nil, getErr
		}
	}
	ws, err := o.newWorkspace(fresh)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	o.open[current.ID] = ws
	o.mu.Unlock()
	return ws, nil
}

func (o *Orchestrator) observations(ctx context.Context, runID string) ([]model.Observation, error) {
	cmds, err := o.store.ListCommands(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []model.Observation
	for _, cmd := range cmds {
		if cmd.State != "succeeded" && cmd.State != "failed" && cmd.State != "denied" {
			continue
		}
		var result struct {
			ExitCode int    `json:"exit_code"`
			Output   string `json:"output"`
		}
		_ = json.Unmarshal(cmd.Result, &result)
		out = append(out, model.Observation{
			Tool:   cmd.Tool,
			Origin: "tool:" + cmd.Tool,
			Exit:   result.ExitCode,
			Output: modelText(result.Output, 4000),
		})
	}
	return out, nil
}

func (o *Orchestrator) fail(ctx context.Context, current store.Run, cause error) error {
	if current.ID == "" {
		return cause
	}
	_ = o.close(ctx, current.ID)
	_ = o.store.SetResult(ctx, current.ID, map[string]any{"error": cause.Error()}, cause.Error())
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return cause
	}
	if run.Can(fresh.State, run.FailedTerminal) {
		_, _ = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.FailedTerminal, "orchestrator")
		_, _ = o.store.AppendEvent(ctx, current.ID, "run.failed", "system", "orchestrator", map[string]any{
			"reason": cause.Error(),
		})
	}
	return nil
}

func (o *Orchestrator) close(ctx context.Context, runID string) error {
	o.mu.Lock()
	ws := o.open[runID]
	delete(o.open, runID)
	o.mu.Unlock()
	if ws == nil {
		return nil
	}
	return ws.Close(ctx)
}

func (o *Orchestrator) flush(ctx context.Context, current store.Run) {
	o.mu.Lock()
	last := o.mark[current.ID]
	o.mu.Unlock()
	events, err := o.store.EventsAfter(ctx, current.ID, last)
	if err != nil {
		return
	}
	for _, ev := range events {
		o.tel.Emit(ctx, telemetry.Event{
			TraceID: current.TraceID,
			RunID:   current.ID,
			Name:    ev.Type,
			Seq:     ev.Seq,
			Attrs:   map[string]string{"actor": ev.ActorType},
		})
		last = ev.Seq
	}
	o.mu.Lock()
	o.mark[current.ID] = last
	o.mu.Unlock()
}

func (o *Orchestrator) seedContext(ctx context.Context, current store.Run) error {
	msgs, err := o.store.ListMessages(ctx, current.ID)
	if err != nil {
		return err
	}
	hasSystem, hasGoal, hasSummary := false, false, false
	for _, msg := range msgs {
		switch {
		case msg.Role == "system":
			hasSystem = true
		case msg.Role == "user" && strings.HasPrefix(msg.Content, "GOAL\n"):
			hasGoal = true
		case msg.Role == "user" && strings.HasPrefix(msg.Content, "DATA origin=repo-summary"):
			hasSummary = true
		}
	}
	if !hasSystem {
		if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{Role: "system", Content: model.SystemPrompt}); err != nil {
			return err
		}
	}
	if !hasGoal {
		if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{Role: "user", Content: "GOAL\n" + current.Goal}); err != nil {
			return err
		}
	}
	if !hasSummary {
		summary, err := workspace.SummarizeRepo(current.RepoPath, 200, 4000)
		if err != nil {
			return err
		}
		if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{Role: "user", Content: summary}); err != nil {
			return err
		}
	}
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	if fresh.State != run.Contextualising {
		return nil
	}
	_, err = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.Planning, "orchestrator")
	return err
}

func (o *Orchestrator) requestTurns(ctx context.Context, current store.Run) ([]model.Turn, error) {
	msgs, err := o.store.ListMessages(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	turns := make([]model.Turn, 0, len(msgs))
	for _, msg := range msgs {
		turns = append(turns, model.Turn{
			Seq: msg.Seq, Role: msg.Role, Content: msg.Content,
			ToolCallID: msg.ToolCallID, ToolName: msg.ToolName, Arguments: msg.Arguments,
		})
	}
	fitted := model.Fit(turns, model.RequestTokenBudget)
	if len(current.Plan) > 0 && string(current.Plan) != "null" {
		fitted = append(fitted, model.Turn{Role: "user", Content: "DATA origin=plan\n" + string(current.Plan)})
	}
	return fitted, nil
}

func (o *Orchestrator) finishUnverified(ctx context.Context, current store.Run, outcome, reason string) error {
	if err := o.close(ctx, current.ID); err != nil {
		return err
	}
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	if err := o.store.SetResult(ctx, current.ID, map[string]any{
		"summary": reason, "outcome": outcome, "provider": current.Provider,
		"tests":        map[string]any{"passed": false},
		"spent_micros": fresh.SpentMicros,
	}, reason); err != nil {
		return err
	}
	fresh, err = o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	if _, err := o.store.Transition(ctx, current.ID, fresh.StateVersion, run.FailedTerminal, "verifier"); err != nil {
		return err
	}
	_, _ = o.store.AppendEvent(ctx, current.ID, "run.failed", "system", "verifier", map[string]any{"reason": reason, "outcome": outcome})
	return nil
}

func (o *Orchestrator) finishVerified(ctx context.Context, current store.Run, outcome, summary, diff, output string, code int, pass bool) error {
	modelName, err := o.latestModelName(ctx, current.ID)
	if err != nil {
		return err
	}
	base, found, err := o.baselineRecord(ctx, current.ID)
	if err != nil {
		return err
	}
	testFiles, err := o.testFilesChanged(ctx, current.ID)
	if err != nil {
		return err
	}
	if !pass {
		if err := o.close(ctx, current.ID); err != nil {
			return err
		}
	}
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	errText := ""
	if !pass {
		errText = summary
	}
	if err := o.store.SetResult(ctx, current.ID, map[string]any{
		"summary": summary, "outcome": outcome, "diff": diff,
		"tests":              map[string]any{"passed": pass, "output": clip(output, 8000), "exit_code": code},
		"baseline":           map[string]any{"recorded": found, "exit_code": base},
		"test_files_changed": testFiles,
		"provider":           current.Provider,
		"model_name":         modelName,
		"spent_micros":       fresh.SpentMicros,
	}, errText); err != nil {
		return err
	}
	fresh, err = o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	next := run.Committing
	if !pass {
		next = run.FailedTerminal
	}
	if _, err := o.store.Transition(ctx, current.ID, fresh.StateVersion, next, "verifier"); err != nil {
		return err
	}
	if !pass {
		_, _ = o.store.AppendEvent(ctx, current.ID, "run.failed", "system", "verifier", map[string]any{
			"reason": summary, "outcome": outcome,
		})
	}
	return nil
}

func (o *Orchestrator) noteBaseline(ctx context.Context, runID, output string, code int) error {
	_, found, err := o.baselineRecord(ctx, runID)
	if err != nil || found {
		return err
	}
	mutated, err := o.hasMutation(ctx, runID)
	if err != nil || mutated {
		return err
	}
	_, err = o.store.AppendEvent(ctx, runID, "baseline.recorded", "system", "verifier", map[string]any{
		"exit_code": code, "fingerprint": fingerprint(output),
	})
	return err
}

func (o *Orchestrator) baselineRecord(ctx context.Context, runID string) (int, bool, error) {
	events, err := o.store.EventsAfter(ctx, runID, 0)
	if err != nil {
		return 0, false, err
	}
	found := false
	exitCode := 0
	for _, ev := range events {
		if ev.Type != "baseline.recorded" {
			continue
		}
		var payload struct {
			Exit int `json:"exit_code"`
		}
		_ = json.Unmarshal(ev.Payload, &payload)
		exitCode = payload.Exit
		found = true
	}
	return exitCode, found, nil
}

func (o *Orchestrator) hasMutation(ctx context.Context, runID string) (bool, error) {
	cmds, err := o.store.ListCommands(ctx, runID)
	if err != nil {
		return false, err
	}
	for _, cmd := range cmds {
		if cmd.State == "succeeded" && isEdit(cmd.Tool) {
			return true, nil
		}
	}
	return false, nil
}

func (o *Orchestrator) testFilesChanged(ctx context.Context, runID string) ([]string, error) {
	cmds, err := o.store.ListCommands(ctx, runID)
	if err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, cmd := range cmds {
		if cmd.State != "succeeded" || !isEdit(cmd.Tool) {
			continue
		}
		rel := commandPath(cmd.Canonical)
		if rel == "" || !tools.IsTestPath(rel) || seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, rel)
	}
	return out, nil
}

func (o *Orchestrator) verificationHistory(ctx context.Context, runID, fp string) (int, bool, error) {
	events, err := o.store.EventsAfter(ctx, runID, 0)
	if err != nil {
		return 0, false, err
	}
	attempts := 0
	seen := false
	for _, ev := range events {
		if ev.Type != "verification.failed" {
			continue
		}
		attempts++
		var payload struct {
			Fingerprint string `json:"fingerprint"`
		}
		_ = json.Unmarshal(ev.Payload, &payload)
		if payload.Fingerprint == fp {
			seen = true
		}
	}
	return attempts, seen, nil
}

func (o *Orchestrator) latestModelName(ctx context.Context, runID string) (string, error) {
	events, err := o.store.EventsAfter(ctx, runID, 0)
	if err != nil {
		return "", err
	}
	name := ""
	for _, ev := range events {
		if ev.Type != "model.called" {
			continue
		}
		var payload struct {
			ModelName string `json:"model_name"`
		}
		_ = json.Unmarshal(ev.Payload, &payload)
		if payload.ModelName != "" {
			name = payload.ModelName
		}
	}
	return name, nil
}

type editRepeat int

const (
	editOK editRepeat = iota
	editDeny
	editStop
)

func (o *Orchestrator) repeatedEdit(ctx context.Context, runID, tool, argsHash string) (editRepeat, error) {
	cmds, err := o.store.ListCommands(ctx, runID)
	if err != nil {
		return editOK, err
	}
	succeeded, denied := 0, 0
	for _, cmd := range cmds {
		if cmd.Tool != tool || cmd.ArgsHash != argsHash {
			continue
		}
		switch cmd.State {
		case "succeeded":
			succeeded++
		case "denied":
			denied++
		}
	}
	if succeeded > 0 && denied > 0 {
		return editStop, nil
	}
	if succeeded > 0 {
		return editDeny, nil
	}
	return editOK, nil
}

func isEdit(tool string) bool {
	return tool == "apply_patch" || tool == "write_file" || tool == "delete_file"
}

func commandPath(raw json.RawMessage) string {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return ""
	}
	rel, err := tools.SafeRel(body.Path)
	if err != nil {
		return ""
	}
	return rel
}

var durationPattern = regexp.MustCompile(`\d+\.\d+s`)

func fingerprint(output string) string {
	text := durationPattern.ReplaceAllString(output, "0s")
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// modelText redacts secrets before a tool result is given to the model
// or copied into an event excerpt. The command row keeps the original text.
func modelText(s string, n int) string {
	return clip(redact.Text(s), n)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// BasePath is the immutable snapshot sibling of a worktree.
func BasePath(work string) string {
	return filepath.Join(filepath.Dir(work), "base")
}
