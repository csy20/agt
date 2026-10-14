package orch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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
			_, err = o.store.Transition(ctx, runID, current.StateVersion, run.Planning, "orchestrator")
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
	resp, err := provider.Complete(ctx, model.Request{
		Goal:           current.Goal,
		System:         model.SystemPrompt,
		Plan:           current.Plan,
		Observations:   obs,
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
		intent.Kind = "tool"
		intent.Tool = resp.Tool
		intent.Canonical = decision.Canonical
		intent.ArgsHash = decision.ArgsHash
		intent.Decision = string(decision.Decision)
		intent.Reason = decision.Reason
		intent.CommandID = id.New("cmd_")
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

func (o *Orchestrator) verify(ctx context.Context, current store.Run) error {
	ws, err := o.workspace(ctx, current)
	if err != nil {
		return err
	}
	output, code, err := ws.Test(ctx)
	if err != nil {
		return err
	}
	diff, diffCode, diffErr := ws.Diff(ctx)
	if diffErr != nil {
		return diffErr
	}
	if diffCode != 0 {
		return fmt.Errorf("git_diff exited %d", diffCode)
	}
	decision, err := policy.Decide("run_test", []byte(`{}`))
	if err != nil {
		return err
	}
	commandID := id.New("cmd_")
	if err := o.store.RecordSystemCommand(ctx, current.ID, commandID, "run_test", decision.ArgsHash, decision.Canonical, output, code); err != nil {
		return err
	}
	passed := code == 0
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	summary := "verification failed"
	errText := ""
	if passed {
		summary = "verification passed"
	} else {
		errText = "tests failed"
	}
	if err := o.store.SetResult(ctx, current.ID, map[string]any{
		"summary":      summary,
		"diff":         diff,
		"tests":        map[string]any{"passed": passed, "output": clip(output, 8000), "exit_code": code},
		"spent_micros": fresh.SpentMicros,
	}, errText); err != nil {
		return err
	}
	if !passed {
		if err := o.close(ctx, current.ID); err != nil {
			return err
		}
		if _, err := o.store.Transition(ctx, current.ID, fresh.StateVersion, run.FailedTerminal, "verifier"); err != nil {
			return err
		}
		_, _ = o.store.AppendEvent(ctx, current.ID, "run.failed", "system", "verifier", map[string]any{
			"reason": "tests failed",
		})
		return nil
	}
	_, err = o.store.Transition(ctx, current.ID, fresh.StateVersion, run.Committing, "verifier")
	return err
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
	if len(out) > 6 {
		out = out[len(out)-6:]
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
