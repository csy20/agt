// Package verify is the user-supplied contract for a feature or refactor run.
// The daemon stores the validated contract. It does not read a host path from
// the request, and a later edit cannot replace the stored checks.
package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/tools"
)

const ContractVersion = "verify.v1"

const (
	TaskRepair   = "repair"
	TaskFeature  = "feature"
	TaskRefactor = "refactor"

	RoleRegression = "regression"
	RoleAcceptance = "acceptance"
	RoleProtected  = "protected"

	KindPass           = "pass"
	KindFail           = "fail"
	KindInfrastructure = "infrastructure"

	OutcomeFeatureImplemented       = "feature_implemented"
	OutcomeAlreadySatisfied         = "already_satisfied"
	OutcomeRegressionBaselineFailed = "regression_baseline_failed"
	OutcomeVerificationInfra        = "verification_infrastructure"
	OutcomeAcceptanceFailed         = "acceptance_failed"
	OutcomeRegressionFailed         = "regression_failed"
	OutcomeFeatureUnchanged         = "feature_unchanged"
	OutcomeContractWeakened         = "contract_weakened"
	OutcomeChecksPassed             = "checks_passed"
	OutcomeRefactorBaselineFailed   = "refactor_baseline_failed"
	OutcomeChecksFailed             = "checks_failed"
	OutcomeRefactorUnchanged        = "refactor_unchanged"
)

// Check is one profile argv. Paths are the check files, not a host location
// the daemon opens while accepting the contract.
type Check struct {
	Name  string   `json:"name"`
	Role  string   `json:"role"`
	Argv  []string `json:"argv"`
	Paths []string `json:"paths"`
}

// Contract is version verify.v1.
type Contract struct {
	Version string  `json:"version"`
	Checks  []Check `json:"checks"`
}

// CheckResult is one verifier execution. Kind pass is a real test pass.
// Kind fail is a real test failure. Infrastructure is neither.
type CheckResult struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Exit   int    `json:"exit_code"`
	Kind   string `json:"kind"`
	Reason string `json:"reason,omitempty"`
	Output string `json:"output,omitempty"`
}

// Decision is the verifier's outcome. Pass is the only success bit.
// Retry means a real test failure may return to the model.
type Decision struct {
	Outcome string
	Summary string
	Pass    bool
	Proceed bool
	Retry   bool
}

// NormalizeTask maps an empty value to repair, which is the historical task.
func NormalizeTask(task string) string {
	task = strings.TrimSpace(task)
	if task == "" {
		return TaskRepair
	}
	return task
}

// ValidTask reports whether a create request may store this task.
func ValidTask(task string) bool {
	switch NormalizeTask(task) {
	case TaskRepair, TaskFeature, TaskRefactor:
		return true
	default:
		return false
	}
}

// NeedsContract reports whether the task must carry a contract and a baseline
// before the model may edit.
func NeedsContract(task string) bool {
	switch NormalizeTask(task) {
	case TaskFeature, TaskRefactor:
		return true
	default:
		return false
	}
}

// Parse validates contract JSON for a task and a canonical profile name.
// raw is the request object. A string, including a filesystem path, is rejected
// and is not opened. Repair omits the contract.
func Parse(raw json.RawMessage, task, profileName string) (Contract, []byte, string, error) {
	task = NormalizeTask(task)
	if !ValidTask(task) {
		return Contract{}, nil, "", fmt.Errorf("task_type must be repair, feature, or refactor")
	}
	trimmed := bytes.TrimSpace(raw)
	if task == TaskRepair {
		if len(trimmed) == 0 || string(trimmed) == "null" {
			return Contract{}, nil, "", nil
		}
		return Contract{}, nil, "", fmt.Errorf("repair does not take a verification contract")
	}
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return Contract{}, nil, "", fmt.Errorf("%s requires a verification contract", task)
	}
	if trimmed[0] != '{' {
		return Contract{}, nil, "", fmt.Errorf("contract must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var in Contract
	if err := dec.Decode(&in); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return Contract{}, nil, "", fmt.Errorf("contract only accepts version and checks")
		}
		return Contract{}, nil, "", fmt.Errorf("contract: %s", err.Error())
	}
	if dec.More() {
		return Contract{}, nil, "", fmt.Errorf("contract must be one object")
	}
	contract, err := validate(in, task, profileName)
	if err != nil {
		return Contract{}, nil, "", err
	}
	canonical, err := canonicalBytes(contract)
	if err != nil {
		return Contract{}, nil, "", err
	}
	return contract, canonical, Digest(canonical), nil
}

// Load reads a contract the daemon already stored. It does not accept a path.
func Load(raw json.RawMessage) (Contract, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return Contract{}, fmt.Errorf("run has no verification contract")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var contract Contract
	if err := dec.Decode(&contract); err != nil {
		return Contract{}, err
	}
	if contract.Version != ContractVersion || len(contract.Checks) == 0 {
		return Contract{}, fmt.Errorf("stored contract is invalid")
	}
	return contract, nil
}

// Digest is the sha256 of the canonical contract. It is the identity of the checks.
func Digest(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// ReadLocal reads a contract file for a client command. The daemon API must
// not call this with a path taken from a request.
func ReadLocal(path string) (json.RawMessage, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("contract file is required")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("contract file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("contract file must be a regular file")
	}
	if info.Size() > 64<<10 {
		return nil, fmt.Errorf("contract file is too large")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("contract file: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 || bytes.TrimSpace(body)[0] != '{' {
		return nil, fmt.Errorf("contract must be an object")
	}
	return json.RawMessage(body), nil
}

func validate(in Contract, task, profileName string) (Contract, error) {
	if in.Version != ContractVersion {
		return Contract{}, fmt.Errorf("contract version must be %s", ContractVersion)
	}
	if len(in.Checks) == 0 {
		return Contract{}, fmt.Errorf("contract checks are empty")
	}
	if len(in.Checks) > 8 {
		return Contract{}, fmt.Errorf("contract is limited to 8 checks")
	}
	if strings.TrimSpace(profileName) == "" {
		return Contract{}, fmt.Errorf("profile is required")
	}
	seen := map[string]bool{}
	var regression, acceptance, protected int
	out := Contract{Version: ContractVersion, Checks: make([]Check, 0, len(in.Checks))}
	for _, check := range in.Checks {
		name, err := cleanName(check.Name)
		if err != nil {
			return Contract{}, err
		}
		if seen[name] {
			return Contract{}, fmt.Errorf("duplicate check %s", name)
		}
		seen[name] = true
		role := strings.TrimSpace(check.Role)
		switch task {
		case TaskFeature:
			if role != RoleRegression && role != RoleAcceptance {
				return Contract{}, fmt.Errorf("unsupported check role %q", role)
			}
		case TaskRefactor:
			if role != RoleProtected {
				return Contract{}, fmt.Errorf("unsupported check role %q", role)
			}
		default:
			return Contract{}, fmt.Errorf("unsupported task %s", task)
		}
		if err := profile.ValidateArgv(profileName, check.Argv); err != nil {
			return Contract{}, fmt.Errorf("check %s: %w", name, err)
		}
		paths, err := cleanPaths(check.Paths)
		if err != nil {
			return Contract{}, fmt.Errorf("check %s: %w", name, err)
		}
		switch role {
		case RoleRegression:
			regression++
		case RoleAcceptance:
			acceptance++
		case RoleProtected:
			protected++
		}
		out.Checks = append(out.Checks, Check{
			Name: name, Role: role, Argv: append([]string(nil), check.Argv...), Paths: paths,
		})
	}
	switch task {
	case TaskFeature:
		if regression == 0 || acceptance == 0 {
			return Contract{}, fmt.Errorf("feature contract requires a regression check and an acceptance check")
		}
	case TaskRefactor:
		if protected == 0 {
			return Contract{}, fmt.Errorf("refactor contract requires a protected check")
		}
	}
	return out, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 40 {
		return "", fmt.Errorf("check name must be 1..40 characters")
	}
	for i, r := range name {
		if i == 0 && !unicode.IsLetter(r) {
			return "", fmt.Errorf("check name must start with a letter")
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return "", fmt.Errorf("check name %q has unsupported characters", name)
		}
	}
	return name, nil
}

func cleanPaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("check paths are empty")
	}
	if len(paths) > 8 {
		return nil, fmt.Errorf("a check is limited to 8 paths")
	}
	out := make([]string, 0, len(paths))
	seen := map[string]bool{}
	for _, raw := range paths {
		if raw == "" || strings.Contains(raw, "\x00") || strings.ContainsAny(raw, `\:`) || strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
			return nil, fmt.Errorf("path %q escapes the worktree", raw)
		}
		rel, err := tools.SafeRel(raw)
		if err != nil {
			return nil, err
		}
		slash := filepath.ToSlash(rel)
		if seen[slash] {
			return nil, fmt.Errorf("duplicate path %s", slash)
		}
		seen[slash] = true
		out = append(out, slash)
	}
	return out, nil
}

func canonicalBytes(contract Contract) ([]byte, error) {
	return json.Marshal(contract)
}

// Protects reports whether a repository-relative path is one of the check files.
func (c Contract) Protects(path string) bool {
	slash := filepath.ToSlash(strings.TrimSpace(path))
	if slash == "" || slash == "." {
		return false
	}
	for _, check := range c.Checks {
		for _, item := range check.Paths {
			if filepath.ToSlash(item) == slash {
				return true
			}
		}
	}
	return false
}

// Classify reads verifier output. A model sentence is not an input.
// An empty run, a missing executable, a timeout, or a runner error is
// infrastructure and is not the intended acceptance failure.
func Classify(exit int, output string, runErr error) (string, string) {
	if runErr != nil {
		msg := strings.ToLower(runErr.Error())
		switch {
		case strings.Contains(msg, "deadline") || strings.Contains(msg, "timeout") || strings.Contains(msg, "context canceled"):
			return KindInfrastructure, "timeout"
		case strings.Contains(msg, "executable file not found") || strings.Contains(msg, "no such file or directory"):
			return KindInfrastructure, "missing executable"
		default:
			return KindInfrastructure, "runner error"
		}
	}
	lower := strings.ToLower(output)
	switch exit {
	case 124, 137:
		return KindInfrastructure, "timeout"
	case 127:
		return KindInfrastructure, "missing executable"
	}
	if strings.Contains(lower, "executable file not found") || strings.Contains(lower, "command not found") {
		return KindInfrastructure, "missing executable"
	}
	if emptyRun(lower) {
		return KindInfrastructure, "empty test run"
	}
	if strings.Contains(output, "ModuleNotFoundError") || strings.Contains(output, "ImportError") {
		return KindInfrastructure, "test target was not executed"
	}
	if strings.TrimSpace(output) == "" || strings.TrimSpace(output) == "(no output)" {
		return KindInfrastructure, "empty test run"
	}
	if exit == 0 {
		if realPass(output) {
			return KindPass, ""
		}
		return KindInfrastructure, "unrecognized test result"
	}
	if realFail(output) {
		return KindFail, ""
	}
	return KindInfrastructure, "unrecognized test result"
}

func emptyRun(lower string) bool {
	for _, pattern := range []string{
		"ran 0 tests",
		"no tests ran",
		"[no test files]",
		"no tests to run",
		"collected 0 items",
		"collected 0 tests",
	} {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func realPass(output string) bool {
	lower := strings.ToLower(output)
	if strings.Contains(lower, "ran ") && !strings.Contains(lower, "ran 0 ") {
		trimmed := strings.TrimSpace(lower)
		return strings.HasSuffix(trimmed, "ok") || strings.Contains(lower, "\nok")
	}
	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "PASS") || strings.Contains(output, "\nPASS\n") || strings.Contains(output, "ok \t") || strings.Contains(output, "ok\t") {
		return true
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "passed") {
			return true
		}
	}
	return false
}

func realFail(output string) bool {
	if strings.Contains(output, "AssertionError") || strings.Contains(output, "FAIL:") || strings.Contains(output, "FAILED") {
		return true
	}
	trimmed := strings.TrimSpace(output)
	if strings.HasPrefix(trimmed, "FAIL") || strings.Contains(output, "\nFAIL\n") || strings.Contains(output, "\nFAIL\t") {
		return true
	}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.EqualFold(line, "failed") || strings.HasPrefix(line, "not ok") {
			return true
		}
	}
	return false
}

// DecideBaseline is the gate before any edit.
func DecideBaseline(task string, checks []CheckResult) Decision {
	task = NormalizeTask(task)
	if reason, ok := firstInfra(checks); ok {
		summary := "a check was not executed: " + reason
		if task == TaskFeature {
			summary = "acceptance was not executed: " + reason
		}
		return Decision{Outcome: OutcomeVerificationInfra, Summary: summary}
	}
	if task == TaskFeature {
		reg := byRole(checks, RoleRegression)
		acc := byRole(checks, RoleAcceptance)
		switch {
		case allKind(reg, KindPass) && allKind(acc, KindPass):
			return Decision{Outcome: OutcomeAlreadySatisfied, Summary: "the requested behavior already passed its acceptance checks. This is not a newly implemented feature."}
		case !allKind(reg, KindPass) && allKind(acc, KindPass):
			return Decision{Outcome: OutcomeRegressionBaselineFailed, Summary: "acceptance checks passed and a regression check failed. This is not a verified feature."}
		case !allKind(reg, KindPass):
			return Decision{Outcome: OutcomeRegressionBaselineFailed, Summary: "a regression check failed before editing. This is not a verified feature."}
		case allKind(acc, KindFail):
			return Decision{Proceed: true, Summary: "regression checks passed and acceptance checks failed before editing"}
		default:
			return Decision{Outcome: OutcomeVerificationInfra, Summary: "acceptance checks were not a real failure before editing"}
		}
	}
	if !allKind(checks, KindPass) {
		return Decision{Outcome: OutcomeRefactorBaselineFailed, Summary: "a baseline check failed. This is not a verified refactor."}
	}
	return Decision{Proceed: true, Summary: "the specified checks passed before editing"}
}

// DecideFinal is the gate after editing. The model does not supply the outcome.
func DecideFinal(task string, checks []CheckResult, protectedChanged, mutated bool) Decision {
	if protectedChanged {
		return Decision{Outcome: OutcomeContractWeakened, Summary: "a protected check changed. The stored contract was not revised. Changing the checks requires a new run and a fresh baseline."}
	}
	if reason, ok := firstInfra(checks); ok {
		return Decision{Outcome: OutcomeVerificationInfra, Summary: "verification was not executed: " + reason}
	}
	if NormalizeTask(task) == TaskFeature {
		reg := byRole(checks, RoleRegression)
		acc := byRole(checks, RoleAcceptance)
		switch {
		case !allKind(reg, KindPass):
			return Decision{Outcome: OutcomeRegressionFailed, Summary: "a regression check failed after editing", Retry: true}
		case !allKind(acc, KindPass):
			return Decision{Outcome: OutcomeAcceptanceFailed, Summary: "an acceptance check failed after editing", Retry: true}
		case !mutated:
			return Decision{Outcome: OutcomeFeatureUnchanged, Summary: "acceptance passed and the worktree did not change. This is not a newly implemented feature."}
		default:
			return Decision{Outcome: OutcomeFeatureImplemented, Summary: "acceptance failed before the edit and passed after it. Regression checks still pass. The verifier executed these checks.", Pass: true}
		}
	}
	if !allKind(checks, KindPass) {
		return Decision{Outcome: OutcomeChecksFailed, Summary: "a specified check failed after editing. This is not a verified refactor.", Retry: true}
	}
	if !mutated {
		return Decision{Outcome: OutcomeRefactorUnchanged, Summary: "the specified checks passed and the worktree did not change. This is not a verified refactor."}
	}
	return Decision{Outcome: OutcomeChecksPassed, Summary: "the specified checks passed before and after the edit. This is not a proof of behavioral equivalence.", Pass: true}
}

// BaselineAllowsFeature reports whether the stored baseline is the required red acceptance.
func BaselineAllowsFeature(checks []CheckResult) bool {
	return DecideBaseline(TaskFeature, checks).Proceed
}

// BaselineAllowsRefactor reports whether every stored baseline check passed.
func BaselineAllowsRefactor(checks []CheckResult) bool {
	return DecideBaseline(TaskRefactor, checks).Proceed
}

func firstInfra(checks []CheckResult) (string, bool) {
	for _, check := range checks {
		if check.Kind == KindInfrastructure {
			reason := check.Reason
			if reason == "" {
				reason = "infrastructure"
			}
			return check.Name + ": " + reason, true
		}
	}
	return "", false
}

func byRole(checks []CheckResult, role string) []CheckResult {
	var out []CheckResult
	for _, check := range checks {
		if check.Role == role {
			out = append(out, check)
		}
	}
	return out
}

func allKind(checks []CheckResult, kind string) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Kind != kind {
			return false
		}
	}
	return true
}

// FailureLines are the checks that did not pass, for the existing status layout.
func FailureLines(checks []CheckResult) []string {
	var out []string
	for _, check := range checks {
		if check.Kind == KindPass {
			continue
		}
		line := check.Role + " " + check.Name + " " + check.Kind
		if check.Reason != "" {
			line += " (" + check.Reason + ")"
		}
		line += fmt.Sprintf(" exit %d", check.Exit)
		out = append(out, line)
	}
	return out
}

// ClipOutput shortens stored verifier text.
func ClipOutput(output string, limit int) string {
	if limit <= 0 || len(output) <= limit {
		return output
	}
	return output[:limit] + "\n…"
}
