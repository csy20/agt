package orch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/patch"
	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/verify"
	"github.com/csy20/agt/internal/workspace"
)

// baselineStored reports whether the pre-edit contract results are on the run.
// A missing value and JSON null both mean the checks have not been recorded.
func baselineStored(raw json.RawMessage) bool {
	text := strings.TrimSpace(string(raw))
	return text != "" && text != "null"
}

// denyProtectedEdit refuses an edit of a check file before it can be approved.
// Approval of some other path does not rewrite the stored contract.
func denyProtectedEdit(current store.Run, tool string, decision *policy.Result) {
	if decision == nil || !verify.NeedsContract(current.TaskType) || !isEdit(tool) {
		return
	}
	contract, err := verify.Load(current.ContractJSON)
	if err != nil || contract.Protects(commandPath(decision.Canonical)) {
		decision.Decision = policy.Deny
		decision.Reason = "this path is protected by the verification contract; changing the checks requires a new run and a fresh baseline"
	}
}

func (o *Orchestrator) contractBaseline(ctx context.Context, current store.Run) error {
	contract, canonical, err := loadStoredContract(current)
	if err != nil {
		return o.finishContract(ctx, current, verify.Decision{
			Outcome: verify.OutcomeVerificationInfra,
			Summary: "the stored contract could not be read: " + err.Error(),
		}, nil, "", "")
	}
	ws, err := o.workspace(ctx, current)
	if err != nil {
		return o.finishContract(ctx, current, verify.Decision{
			Outcome: verify.OutcomeVerificationInfra,
			Summary: "workspace: " + err.Error(),
		}, nil, "", "")
	}
	checks, err := o.runChecks(ctx, current, contract, ws)
	if err != nil {
		return err
	}
	body, err := evidenceJSON("baseline", checks)
	if err != nil {
		return err
	}
	if err := o.store.SetBaseline(ctx, current.ID, body); err != nil {
		return err
	}
	decision := verify.DecideBaseline(current.TaskType, checks)
	if _, err := o.store.AppendEvent(ctx, current.ID, "contract.baseline", "system", "verifier", map[string]any{
		"outcome": decision.Outcome, "proceed": decision.Proceed, "summary": decision.Summary,
		"checks": checkLines(checks), "failures": verify.FailureLines(checks),
		"contract_digest": verify.Digest(canonical),
	}); err != nil {
		return err
	}
	art, diff, reviewErr := o.reviewWork(ctx, current, ws)
	if !decision.Proceed {
		if reviewErr != nil {
			diff = ""
		}
		final, err := evidenceJSON("final", nil)
		if err != nil {
			return err
		}
		return o.finishContract(ctx, current, decision, final, diff, art.Digest)
	}
	if reviewErr != nil {
		return reviewErr
	}
	if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{
		Role:    "user",
		Content: verificationNote("baseline", decision.Summary, checks),
	}); err != nil {
		return err
	}
	return nil
}

func (o *Orchestrator) verifyContract(ctx context.Context, current store.Run) error {
	contract, _, err := loadStoredContract(current)
	if err != nil {
		return o.finishContract(ctx, current, verify.Decision{
			Outcome: verify.OutcomeVerificationInfra,
			Summary: "the stored contract could not be read: " + err.Error(),
		}, nil, "", "")
	}
	ws, err := o.workspace(ctx, current)
	if err != nil {
		return o.finishContract(ctx, current, verify.Decision{
			Outcome: verify.OutcomeVerificationInfra,
			Summary: "workspace: " + err.Error(),
		}, nil, "", "")
	}
	art, diff, reviewErr := o.reviewWork(ctx, current, ws)
	if reviewErr != nil && art.Digest == "" {
		return o.finishContract(ctx, current, verify.Decision{
			Outcome: verify.OutcomeVerificationInfra,
			Summary: "the patch artifact was not built: " + reviewErr.Error(),
		}, nil, "", "")
	}
	protected, mutated := protectedMutation(contract, art)
	var checks []verify.CheckResult
	if !protected {
		checks, err = o.runChecks(ctx, current, contract, ws)
		if err != nil {
			return err
		}
	}
	decision := verify.DecideFinal(current.TaskType, checks, protected, mutated)
	if decision.Pass && !baselineAllows(current) {
		decision.Pass = false
		decision.Outcome = verify.OutcomeVerificationInfra
		decision.Summary = "the stored baseline does not allow this verification claim"
		decision.Retry = false
	}
	if reviewErr != nil && decision.Pass {
		decision.Pass = false
		decision.Outcome = verify.OutcomeVerificationInfra
		decision.Summary = "the diff was not available: " + reviewErr.Error()
		decision.Retry = false
		diff = ""
	}
	evidence, err := evidenceJSON("final", checks)
	if err != nil {
		return err
	}
	if decision.Retry {
		terminal, err := o.contractRetry(ctx, current, decision, checks, evidence, art.Digest)
		if err != nil || !terminal {
			return err
		}
	}
	return o.finishContract(ctx, current, decision, evidence, diff, art.Digest)
}

// contractRetry records one real test failure and returns to the model when
// the same output has not already been seen. Infrastructure and a weakened
// contract do not reach this path. The outcome label stays the contract label.
func (o *Orchestrator) contractRetry(ctx context.Context, current store.Run, decision verify.Decision, checks []verify.CheckResult, evidence []byte, digest string) (bool, error) {
	fp := fingerprint(joinedChecks(checks))
	attempts, seen, err := o.verificationHistory(ctx, current.ID, fp)
	if err != nil {
		return false, err
	}
	terminal := seen || attempts >= maxVerificationReturns
	if !terminal && current.MaxSteps >= 0 && current.StepCount >= current.MaxSteps {
		terminal = true
	}
	if !terminal {
		if _, err := o.store.AppendEvent(ctx, current.ID, "verification.failed", "system", "verifier", map[string]any{
			"fingerprint": fp, "attempt": attempts + 1, "outcome": decision.Outcome,
		}); err != nil {
			return false, err
		}
		if err := o.store.SetVerification(ctx, current.ID, evidence, decision.Outcome, digest); err != nil {
			return false, err
		}
		if err := o.store.AppendMessage(ctx, current.ID, store.TranscriptMessage{
			Role:    "user",
			Content: verificationNote("final", decision.Summary, checks),
		}); err != nil {
			return false, err
		}
		fresh, err := o.store.GetRun(ctx, current.ID)
		if err != nil {
			return false, err
		}
		if _, err := o.store.Transition(ctx, current.ID, fresh.StateVersion, run.Observing, "verifier"); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

func (o *Orchestrator) finishContract(ctx context.Context, current store.Run, decision verify.Decision, evidence []byte, diff, artDigest string) error {
	if !decision.Pass {
		if err := o.closeBounded(current.ID); err != nil {
			return err
		}
	}
	if len(evidence) == 0 {
		var err error
		evidence, err = evidenceJSON("final", nil)
		if err != nil {
			return err
		}
	}
	if err := o.store.SetVerification(ctx, current.ID, evidence, decision.Outcome, artDigest); err != nil {
		return err
	}
	modelName, err := o.latestModelName(ctx, current.ID)
	if err != nil {
		return err
	}
	fresh, err := o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	errText := ""
	if !decision.Pass {
		errText = decision.Summary
	}
	if err := o.store.SetResult(ctx, current.ID, map[string]any{
		"summary": decision.Summary, "outcome": decision.Outcome, "diff": diff,
		"task_type":           verify.NormalizeTask(current.TaskType),
		"contract_digest":     current.ContractDigest,
		"verification_digest": artDigest,
		"tests":               map[string]any{"passed": decision.Pass, "invoked": true},
		"provider":            current.Provider,
		"model_name":          modelName,
		"spent_micros":        fresh.SpentMicros,
	}, errText); err != nil {
		return err
	}
	fresh, err = o.store.GetRun(ctx, current.ID)
	if err != nil {
		return err
	}
	next := run.Committing
	if !decision.Pass {
		next = run.FailedTerminal
	}
	if _, err := o.store.Transition(ctx, current.ID, fresh.StateVersion, next, "verifier"); err != nil {
		return err
	}
	recorded := map[string]any{
		"outcome": decision.Outcome, "summary": decision.Summary, "passed": decision.Pass,
		"digest": artDigest, "contract_digest": current.ContractDigest,
	}
	if lines := failureLinesFrom(evidence); len(lines) > 0 {
		recorded["failures"] = lines
	} else if decision.Pass {
		recorded["failures"] = []string{}
	}
	_, _ = o.store.AppendEvent(ctx, current.ID, "verification.recorded", "system", "verifier", recorded)
	if !decision.Pass {
		_, _ = o.store.AppendEvent(ctx, current.ID, "run.failed", "system", "verifier", map[string]any{
			"reason": decision.Summary, "outcome": decision.Outcome,
		})
		o.recordSessionResult(ctx, current.ID)
	}
	return nil
}

func (o *Orchestrator) runChecks(ctx context.Context, current store.Run, contract verify.Contract, ws workspace.Handle) ([]verify.CheckResult, error) {
	results := make([]verify.CheckResult, 0, len(contract.Checks))
	for _, check := range contract.Checks {
		if stop := contextStop(ctx); stop != nil {
			return nil, stop
		}
		output, code, runErr := ws.Check(ctx, check.Argv)
		if stop := contextStop(ctx); stop != nil {
			return nil, stop
		}
		if runErr != nil && strings.TrimSpace(output) == "" {
			output = runErr.Error()
		}
		kind, reason := verify.Classify(code, output, runErr)
		result := verify.CheckResult{
			Name: check.Name, Role: check.Role, Exit: code, Kind: kind, Reason: reason,
			Output: verify.ClipOutput(output, 2000),
		}
		canonical, err := json.Marshal(struct {
			Name string   `json:"name"`
			Role string   `json:"role"`
			Argv []string `json:"argv"`
		}{check.Name, check.Role, check.Argv})
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(canonical)
		if err := o.store.RecordSystemCommand(ctx, current.ID, id.New("cmd_"), "run_test", hex.EncodeToString(sum[:]), canonical, output, code); err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (o *Orchestrator) reviewWork(ctx context.Context, current store.Run, ws workspace.Handle) (patch.Artifact, string, error) {
	captured := workspace.ReadCapture(current.RepoPath)
	art, err := ws.Artifact(patch.Meta{
		RunID:    current.ID,
		RepoPath: current.RepoPath,
		Revision: captured.Revision,
	})
	if err != nil {
		return patch.Artifact{}, "", err
	}
	diff, code, err := ws.Diff(ctx)
	if err != nil {
		return art, "", err
	}
	if code != 0 {
		return art, "", fmt.Errorf("git_diff exited %d", code)
	}
	return art, diff, nil
}

func loadStoredContract(current store.Run) (verify.Contract, []byte, error) {
	contract, err := verify.Load(current.ContractJSON)
	if err != nil {
		return verify.Contract{}, nil, err
	}
	canonical, err := json.Marshal(contract)
	if err != nil {
		return verify.Contract{}, nil, err
	}
	if verify.Digest(canonical) != current.ContractDigest {
		return verify.Contract{}, nil, fmt.Errorf("stored contract does not match its digest")
	}
	return contract, canonical, nil
}

func baselineAllows(current store.Run) bool {
	var doc struct {
		Checks []verify.CheckResult `json:"checks"`
	}
	if err := json.Unmarshal(current.BaselineJSON, &doc); err != nil {
		return false
	}
	switch verify.NormalizeTask(current.TaskType) {
	case verify.TaskFeature:
		return verify.BaselineAllowsFeature(doc.Checks)
	case verify.TaskRefactor:
		return verify.BaselineAllowsRefactor(doc.Checks)
	default:
		return false
	}
}

func protectedMutation(contract verify.Contract, art patch.Artifact) (protected, mutated bool) {
	for _, file := range art.Files {
		if contract.Protects(file.Path) {
			protected = true
			continue
		}
		mutated = true
	}
	return protected, mutated
}

func evidenceJSON(phase string, checks []verify.CheckResult) ([]byte, error) {
	if checks == nil {
		checks = []verify.CheckResult{}
	}
	return json.Marshal(struct {
		Phase  string               `json:"phase"`
		Checks []verify.CheckResult `json:"checks"`
	}{Phase: phase, Checks: checks})
}

func failureLinesFrom(evidence []byte) []string {
	var doc struct {
		Checks []verify.CheckResult `json:"checks"`
	}
	if json.Unmarshal(evidence, &doc) != nil {
		return nil
	}
	return verify.FailureLines(doc.Checks)
}

func checkLines(checks []verify.CheckResult) []string {
	lines := make([]string, 0, len(checks))
	for _, check := range checks {
		lines = append(lines, fmt.Sprintf("%s %s %s exit %d", check.Role, check.Name, check.Kind, check.Exit))
	}
	return lines
}

func joinedChecks(checks []verify.CheckResult) string {
	var b strings.Builder
	for _, check := range checks {
		fmt.Fprintf(&b, "%s %s %d\n%s\n", check.Name, check.Kind, check.Exit, check.Output)
	}
	return b.String()
}

func verificationNote(phase, summary string, checks []verify.CheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "DATA origin=verification\nphase=%s\n%s\n", phase, summary)
	for _, check := range checks {
		fmt.Fprintf(&b, "%s %s %s exit %d\n", check.Role, check.Name, check.Kind, check.Exit)
		if check.Reason != "" {
			fmt.Fprintf(&b, "reason %s\n", check.Reason)
		}
		if check.Output != "" {
			b.WriteString(verify.ClipOutput(check.Output, 500))
			b.WriteByte('\n')
		}
	}
	b.WriteString("The stored contract is fixed for this run. Editing a check file is denied and does not revise it.\n")
	return b.String()
}
