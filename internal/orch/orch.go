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
	"github.com/csy20/agt/internal/patch"
	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/profile"
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
	// ErrPaused means a human, budget, or cancel stop should close the sandbox
	// and finish the job without requeueing the run.
	ErrPaused = errors.New("run paused")
	// ErrLeaseLost means another worker holds the job. Do not release it or
	// destroy the sandbox; the name is shared with the new owner.
	ErrLeaseLost = errors.New("lease lost")
)

// reservedOutputTokens matches the OpenAI adapter default when a request does
// not set a smaller cap. It is an estimate, not a provider guarantee.
const reservedOutputTokens = 2048

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
		if err := ctx.Err(); err != nil {
			return o.onStop(ctx, runID, err)
		}
		current, err := o.store.GetRun(ctx, runID)
		if err != nil {
			return err
		}
		o.flush(ctx, current)
		if run.IsTerminal(current.State) {
			_ = o.closeBounded(runID)
			return nil
		}
		if !current.Deadline.IsZero() && time.Now().After(current.Deadline) && current.State != run.PausedBudget && current.State != run.Paused {
			return o.deadlineStop(ctx, current)
		}
		stepCtx, cancelStep := stepContext(ctx, current)
		err = o.executeOpen(stepCtx, current)
		if err == nil {
			current, err = o.store.GetRun(stepCtx, runID)
		}
		if err == nil && !run.IsTerminal(current.State) {
			err = o.dispatch(stepCtx, current)
		}
		cancelStep()
		if stop, ret := o.classify(ctx, current, err); stop {
			return ret
		}
		if err != nil {
			return o.fail(ctx, current, err)
		}
	}
	current, _ := o.store.GetRun(ctx, runID)
	return o.fail(ctx, current, fmt.Errorf("orchestrator loop exceeded"))
}

func (o *Orchestrator) dispatch(ctx context.Context, current store.Run) error {
	switch current.State {
	case run.AwaitingApproval, run.Paused, run.PausedBudget:
		return ErrHalt
	case run.Created:
		_, err := o.store.Transition(ctx, current.ID, current.StateVersion, run.Contextualising, "orchestrator")
		return err
	case run.Contextualising:
		return o.seedContext(ctx, current)
	case run.Planning, run.Observing:
		return o.modelTurn(ctx, current)
	case run.Verifying:
		return o.verify(ctx, current)
	case run.Executing:
		_, openErr := o.store.OpenCommand(ctx, current.ID)
		if errors.Is(openErr, store.ErrNotFound) {
			_, err := o.store.Transition(ctx, current.ID, current.StateVersion, run.Observing, "orchestrator")
			return err
		}
		return openErr
	case run.Committing:
		// Destroy the sandbox before the terminal state is visible. Cleanup
		// uses its own deadline so a client disconnect cannot interrupt it.
		if err := o.closeBounded(current.ID); err != nil {
			return o.fail(ctx, current, err)
		}
		if _, err := o.store.Transition(ctx, current.ID, current.StateVersion, run.Succeeded, "orchestrator"); err != nil {
			return o.fail(ctx, current, err)
		}
		_, _ = o.store.AppendEvent(ctx, current.ID, "run.succeeded", "system", "orchestrator", map[string]any{
			"run_id": current.ID,
		})
		o.recordSessionResult(ctx, current.ID)
		return nil
	default:
		return fmt.Errorf("no handler for state %s", current.State)
	}
}

func stepContext(parent context.Context, current store.Run) (context.Context, context.CancelFunc) {
	if current.Deadline.IsZero() || !time.Now().Before(current.Deadline) {
		return parent, func() {}
	}
	return context.WithDeadline(parent, current.Deadline)
}

func (o *Orchestrator) onStop(ctx context.Context, runID string, err error) error {
	if errors.Is(context.Cause(ctx), ErrPaused) || errors.Is(err, ErrPaused) {
		_ = o.closeBounded(runID)
		return nil
	}
	if errors.Is(context.Cause(ctx), ErrLeaseLost) || errors.Is(err, ErrLeaseLost) {
		return ErrLeaseLost
	}
	return err
}

func (o *Orchestrator) classify(parent context.Context, current store.Run, err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if errors.Is(err, ErrHalt) {
		return true, nil
	}
	if errors.Is(err, ErrInterrupt) || errors.Is(err, ErrLeaseLost) {
		return true, err
	}
	if errors.Is(err, ErrPaused) || errors.Is(context.Cause(parent), ErrPaused) {
		_ = o.closeBounded(current.ID)
		return true, nil
	}
	if errors.Is(context.Cause(parent), ErrLeaseLost) {
		return true, ErrLeaseLost
	}
	if parent.Err() != nil && (errors.Is(err, context.Canceled) || errors.Is(err, parent.Err())) {
		return true, parent.Err()
	}
	if errors.Is(err, context.DeadlineExceeded) && parent.Err() == nil {
		return true, o.deadlineStop(parent, current)
	}
	if errors.Is(err, store.ErrConflict) {
		fresh, getErr := o.store.GetRun(context.Background(), current.ID)
		if getErr == nil && (run.Quiescent(fresh.State) || run.IsTerminal(fresh.State)) {
			_ = o.closeBounded(current.ID)
			return true, nil
		}
	}
	return false, err
}

func (o *Orchestrator) deadlineStop(ctx context.Context, current store.Run) error {
	_ = o.closeBounded(current.ID)
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return nil
	}
	if fresh.State == run.PausedBudget || fresh.State == run.Paused || run.IsTerminal(fresh.State) || run.Quiescent(fresh.State) {
		return nil
	}
	if !run.Can(fresh.State, run.PausedBudget) {
		return nil
	}
	if _, err := o.store.Transition(ctx, current.ID, fresh.StateVersion, run.PausedBudget, "deadline"); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil
		}
		return err
	}
	return nil
}

func contextStop(ctx context.Context) error {
	if ctx.Err() == nil {
		return nil
	}
	switch {
	case errors.Is(context.Cause(ctx), ErrPaused):
		return ErrPaused
	case errors.Is(context.Cause(ctx), ErrLeaseLost):
		return ErrLeaseLost
	default:
		return ctx.Err()
	}
}

func (o *Orchestrator) modelTurn(ctx context.Context, current store.Run) error {
	if current.MaxSteps >= 0 && current.StepCount >= current.MaxSteps {
		return o.pauseForBudget(ctx, current, "step budget exhausted", 0)
	}
	turns, err := o.requestTurns(ctx, current)
	if err != nil {
		return err
	}
	// Reserve is a pre-check, not a ledger hold. A provider that reports no
	// usage, or prices that are both zero, cannot enforce a dollar cap.
	reserve := o.reserveMicros(turns)
	limit := usage.CentsToMicros(int64(current.BudgetCents))
	if reserve > 0 && current.SpentMicros+reserve > limit {
		return o.pauseForBudget(ctx, current, "reserved model cost exceeds the budget", reserve)
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
	resp, err := provider.Complete(ctx, model.Request{
		Goal:           current.Goal,
		System:         model.SystemPrompt,
		Plan:           current.Plan,
		Observations:   obs,
		Transcript:     turns,
		CompletedCalls: completed,
		// Text may be stored before this call returns. The tool is not executed
		// until CommitModelIntent, and only when the response carried a complete call.
		Display: func(text string) {
			_ = o.store.AppendDisplay(ctx, current.ID, text)
		},
	})
	if err != nil {
		o.noteAttempts(ctx, current, err)
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
		Attempts:     resp.Attempts,
	}
	if current.SpentMicros+cost > limit {
		intent.Kind = "budget"
	} else if resp.Finish {
		intent.Kind = "finish"
	} else if resp.Tool == "" && len(resp.Plan) > 0 {
		intent.Kind = "plan"
	} else if resp.Tool == "" {
		return fmt.Errorf("model returned no action")
	} else {
		decision, err := policy.DecideFor(current.Interaction, resp.Tool, resp.Args)
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
	if _, err = o.store.CommitModelIntent(ctx, current.ID, current.StateVersion, intent); err != nil {
		return err
	}
	if intent.Kind == "budget" {
		_ = o.closeBounded(current.ID)
	}
	return nil
}

func (o *Orchestrator) reserveMicros(turns []model.Turn) int64 {
	if o.prices.InputPerMillion <= 0 && o.prices.OutputPerMillion <= 0 {
		return 0
	}
	tokens := 0
	for _, turn := range turns {
		tokens += model.EstimateTokens(turn.Content)
		tokens += model.EstimateTokens(turn.Arguments)
		tokens += model.EstimateTokens(turn.ToolName)
	}
	return usage.CostMicros(int64(tokens), o.prices.InputPerMillion) +
		usage.CostMicros(reservedOutputTokens, o.prices.OutputPerMillion)
}

func (o *Orchestrator) pauseForBudget(ctx context.Context, current store.Run, reason string, reserved int64) error {
	_ = o.closeBounded(current.ID)
	if _, err := o.store.Transition(ctx, current.ID, current.StateVersion, run.PausedBudget, "budget"); err != nil {
		return err
	}
	_, _ = o.store.AppendEvent(ctx, current.ID, "budget.paused", "system", "budget", map[string]any{
		"spent_micros":    current.SpentMicros,
		"budget_cents":    current.BudgetCents,
		"reserved_micros": reserved,
		"reason":          reason,
	})
	return nil
}

func (o *Orchestrator) noteAttempts(ctx context.Context, current store.Run, err error) {
	var status *model.StatusError
	if !errors.As(err, &status) || status.Attempts <= 1 || ctx.Err() != nil {
		return
	}
	_ = o.store.AddUsage(ctx, current.ID, "model.http_attempts", int64(status.Attempts), 0, status.Model, profileLabel(current.ProfileJSON))
}

func profileLabel(raw json.RawMessage) string {
	prof, err := profile.Parse(raw)
	if err != nil {
		return ""
	}
	return prof.Name
}

func (o *Orchestrator) executeOpen(ctx context.Context, current store.Run) error {
	if stop := contextStop(ctx); stop != nil {
		return stop
	}
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
	// A pause or a lost lease leaves the command pending. apply_patch is
	// idempotent and write_file/delete_file are hash-preconditioned, so resume
	// reconciles instead of assuming the side effect did not happen.
	if stop := contextStop(ctx); stop != nil {
		return stop
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
	switch policy.NormalizeInteraction(current.Interaction) {
	case "ask", "plan":
		// An answer is the terminal result. Repair verification, including the
		// baseline test, is not part of these modes.
		return o.finishAnswer(ctx, current)
	}
	ws, err := o.workspace(ctx, current)
	if err != nil {
		return o.finishUnverified(ctx, current, "verification_unavailable", "workspace: "+err.Error())
	}
	output, code, err := ws.Test(ctx)
	if err != nil {
		if stop := contextStop(ctx); stop != nil {
			return stop
		}
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
		captured := workspace.ReadCapture(current.RepoPath)
		if _, err := o.store.AppendEvent(ctx, current.ID, "workspace.captured", "system", "orchestrator", map[string]any{
			"strategy":             patch.Strategy,
			"revision":             captured.Revision,
			"ref":                  captured.Ref,
			"includes_uncommitted": true,
			"note":                 "snapshot of the working tree, including uncommitted files; .git was not copied",
		}); err != nil {
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
	_ = o.closeBounded(current.ID)
	_ = o.store.SetResult(ctx, current.ID, map[string]any{"error": cause.Error(), "category": failureCategory(cause)}, cause.Error())
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return cause
	}
	if run.Can(fresh.State, run.FailedTerminal) {
		_, _ = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.FailedTerminal, "orchestrator")
		_, _ = o.store.AppendEvent(ctx, current.ID, "run.failed", "system", "orchestrator", map[string]any{
			"reason": cause.Error(), "category": failureCategory(cause),
		})
	}
	o.recordSessionResult(ctx, current.ID)
	return nil
}

func failureCategory(cause error) string {
	msg := strings.ToLower(cause.Error())
	switch {
	case strings.Contains(msg, "lease"):
		return "lease"
	case strings.Contains(msg, "docker") || strings.Contains(msg, "sandbox"):
		return "sandbox"
	case strings.Contains(msg, "workspace") || strings.Contains(msg, "snapshot"):
		return "workspace"
	default:
		return "supervisor"
	}
}

func (o *Orchestrator) closeBounded(runID string) error {
	stop, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return o.close(stop, runID)
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
	hasSystem, hasGoal, hasSummary, hasHistory := false, false, false, false
	for _, msg := range msgs {
		switch {
		case msg.Role == "system":
			hasSystem = true
		case msg.Role == "user" && strings.HasPrefix(msg.Content, "GOAL\n"):
			hasGoal = true
		case msg.Role == "user" && strings.HasPrefix(msg.Content, "DATA origin=repo-summary"):
			hasSummary = true
		case msg.Role == "user" && strings.HasPrefix(msg.Content, "DATA origin=session-history"):
			hasHistory = true
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
	if current.SessionID != "" && !hasHistory {
		history, err := o.sessionHistory(ctx, current)
		if err != nil {
			return err
		}
		if history != "" {
			if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{Role: "user", Content: history}); err != nil {
				return err
			}
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

func (o *Orchestrator) sessionHistory(ctx context.Context, current store.Run) (string, error) {
	msgs, err := o.store.ListSessionMessages(ctx, current.SessionID)
	if err != nil {
		return "", err
	}
	facts := make([]model.SessionFact, 0, len(msgs))
	for _, msg := range msgs {
		if msg.RunID == current.ID && msg.Kind == "user" {
			continue
		}
		facts = append(facts, model.SessionFact{
			Seq: msg.Seq, Role: msg.Role, Kind: msg.Kind, Content: msg.Content, RunID: msg.RunID,
		})
	}
	return model.SessionHistory(facts, 1500), nil
}

func (o *Orchestrator) recordSessionResult(ctx context.Context, runID string) {
	current, err := o.store.GetRun(ctx, runID)
	if err != nil || current.SessionID == "" {
		return
	}
	_ = o.store.RecordSessionResult(ctx, current)
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

// finishAnswer records the model's text and moves the run to COMMITTING.
// It does not open a test sandbox and does not record a baseline.
func (o *Orchestrator) finishAnswer(ctx context.Context, current store.Run) error {
	msgs, err := o.store.ListMessages(ctx, current.ID)
	if err != nil {
		return err
	}
	text := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && msgs[i].ToolName == "" && strings.TrimSpace(msgs[i].Content) != "" {
			text = msgs[i].Content
			break
		}
	}
	if strings.TrimSpace(text) == "" {
		return o.finishUnverified(ctx, current, "answer_missing", "ask and plan finished without an answer")
	}
	modelName, err := o.latestModelName(ctx, current.ID)
	if err != nil {
		return err
	}
	if err := o.closeBounded(current.ID); err != nil {
		return err
	}
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	mode := policy.NormalizeInteraction(current.Interaction)
	if err := o.store.SetResult(ctx, current.ID, map[string]any{
		"summary": text, "outcome": "answered", "provider": current.Provider,
		"model_name":   modelName,
		"interaction":  mode,
		"tests":        map[string]any{"invoked": false},
		"spent_micros": fresh.SpentMicros,
	}, ""); err != nil {
		return err
	}
	fresh, err = o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	_, err = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.Committing, "orchestrator")
	return err
}

func (o *Orchestrator) finishUnverified(ctx context.Context, current store.Run, outcome, reason string) error {
	if err := o.closeBounded(current.ID); err != nil {
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
	o.recordSessionResult(ctx, current.ID)
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
		if err := o.closeBounded(current.ID); err != nil {
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
