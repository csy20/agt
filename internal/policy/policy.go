package policy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/csy20/agt/internal/tools"
)

const Version = "policy.v1"

type Decision string

const (
	Allow           Decision = "allow"
	Deny            Decision = "deny"
	RequireApproval Decision = "require_approval"
)

// Result is a deterministic authority decision. The model does not produce it.
type Result struct {
	Tool      string
	Decision  Decision
	Canonical []byte
	ArgsHash  string
	Reason    string
}

// Decide validates tool arguments and assigns a risk decision.
// Invalid or unknown tools are denied. They never become an execution.
func Decide(tool string, raw json.RawMessage) (Result, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte(`{}`)
	}
	switch tool {
	case "search_code":
		var args tools.SearchArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		if strings.TrimSpace(args.Query) == "" || len(args.Query) > 200 {
			body, _ := json.Marshal(args)
			return denied(tool, body, "query must be 1..200 characters"), nil
		}
		body, err := json.Marshal(args)
		if err != nil {
			return Result{}, err
		}
		return allowed(tool, body, "read"), nil
	case "read_file_range":
		var args tools.ReadArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		rel, err := tools.SafeRel(args.Path)
		if err != nil {
			body, _ := json.Marshal(args)
			return denied(tool, body, err.Error()), nil
		}
		if tools.SecretRel(rel) {
			body, _ := json.Marshal(args)
			return denied(tool, body, "secret paths are not available to the agent"), nil
		}
		if args.Start < 1 || args.End < args.Start || args.End-args.Start > 399 {
			body, _ := json.Marshal(args)
			return denied(tool, body, "line range must be within 400 lines"), nil
		}
		body, err := json.Marshal(args)
		if err != nil {
			return Result{}, err
		}
		return allowed(tool, body, "read"), nil
	case "git_diff":
		var args tools.EmptyArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		body, _ := json.Marshal(args)
		return allowed(tool, body, "read"), nil
	case "run_test":
		var args tools.EmptyArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		body, _ := json.Marshal(args)
		return allowed(tool, body, "ephemeral compute inside the sandbox"), nil
	case "apply_patch":
		var args tools.PatchArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		rel, err := tools.SafeRel(args.Path)
		if err != nil {
			full, _ := json.Marshal(args)
			return denied(tool, full, err.Error()), nil
		}
		if tools.SecretRel(rel) {
			full, _ := json.Marshal(args)
			return denied(tool, full, "secret paths are not available to the agent"), nil
		}
		if args.Old == "" || args.New == "" || len(args.Old) > 200_000 || len(args.New) > 200_000 {
			body, _ := json.Marshal(args)
			return denied(tool, body, "old and new text are required and must be under 200KB"), nil
		}
		body, err := json.Marshal(args)
		if err != nil {
			return Result{}, err
		}
		return allowed(tool, body, "workspace write inside the run worktree"), nil
	case "write_file":
		var write tools.WriteArgs
		if err := decode(raw, &write); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		rel, err := tools.SafeRel(write.Path)
		if err != nil {
			body, _ := json.Marshal(write)
			return denied(tool, body, err.Error()), nil
		}
		if tools.SecretRel(rel) {
			body, _ := json.Marshal(write)
			return denied(tool, body, "secret paths are not available to the agent"), nil
		}
		if strings.Contains(write.Content, "\x00") || len(write.Content) > 200_000 {
			body, _ := json.Marshal(write)
			return denied(tool, body, "content must be text under 200KB"), nil
		}
		if !shaOK(write.BeforeSHA256, false) {
			body, _ := json.Marshal(write)
			return denied(tool, body, "before_sha256 must be empty or 64 lowercase hex characters"), nil
		}
		body, err := json.Marshal(write)
		if err != nil {
			return Result{}, err
		}
		return allowed(tool, body, "workspace write inside the run worktree"), nil
	case "delete_file":
		var del tools.DeleteArgs
		if err := decode(raw, &del); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		rel, err := tools.SafeRel(del.Path)
		if err != nil {
			body, _ := json.Marshal(del)
			return denied(tool, body, err.Error()), nil
		}
		if tools.SecretRel(rel) {
			body, _ := json.Marshal(del)
			return denied(tool, body, "secret paths are not available to the agent"), nil
		}
		if !shaOK(del.BeforeSHA256, true) {
			body, _ := json.Marshal(del)
			return denied(tool, body, "before_sha256 must be 64 lowercase hex characters"), nil
		}
		body, err := json.Marshal(del)
		if err != nil {
			return Result{}, err
		}
		return allowed(tool, body, "workspace delete inside the run worktree"), nil
	case "git_push":
		var args tools.PushArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		if !token(args.Remote) || !token(args.Branch) || !token(args.Commit) {
			body, _ := json.Marshal(args)
			return denied(tool, body, "remote, branch, and commit must be single tokens"), nil
		}
		body, err := json.Marshal(args)
		if err != nil {
			return Result{}, err
		}
		return denied(tool, body, "unsupported: git push is not available and no remote process is spawned"), nil
	case "pr_open":
		var args tools.PRArgs
		if err := decode(raw, &args); err != nil {
			return denied(tool, raw, "invalid arguments"), nil
		}
		if !token(args.Branch) || strings.TrimSpace(args.Title) == "" || len(args.Title) > 200 {
			body, _ := json.Marshal(args)
			return denied(tool, body, "branch and title are required"), nil
		}
		body, err := json.Marshal(args)
		if err != nil {
			return Result{}, err
		}
		return denied(tool, body, "unsupported: opening a pull request is not available and no remote process is spawned"), nil
	default:
		return denied(tool, raw, "unknown tool"), nil
	}
}

func shaOK(s string, required bool) bool {
	if s == "" {
		return !required
	}
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func token(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	return !strings.ContainsAny(s, " \t\r\n")
}

func decode(raw []byte, dest any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func allowed(tool string, canonical []byte, reason string) Result {
	return Result{Tool: tool, Decision: Allow, Canonical: canonical, ArgsHash: hash(tool, canonical), Reason: reason}
}

// approval builds a require_approval decision. No tool returns one in this
// build: git_push and pr_open are denied as unsupported. The helper stays so
// a later exact-action gate uses the same hash binding.
func approval(tool string, canonical []byte, reason string) Result {
	return Result{Tool: tool, Decision: RequireApproval, Canonical: canonical, ArgsHash: hash(tool, canonical), Reason: reason}
}

func denied(tool string, canonical []byte, reason string) Result {
	return Result{Tool: tool, Decision: Deny, Canonical: canonical, ArgsHash: hash(tool, canonical), Reason: reason}
}

func hash(tool string, canonical []byte) string {
	sum := sha256.Sum256(append([]byte(tool+"\n"), canonical...))
	return hex.EncodeToString(sum[:])
}
