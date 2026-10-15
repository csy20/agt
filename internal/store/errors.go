package store

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrIdempotency  = errors.New("idempotency key reused with a different request")
	ErrHashMismatch = errors.New("approval does not match the canonical command")
	ErrSessionBusy  = errors.New("a run is already active in this session")
	ErrRepoMismatch = errors.New("session is bound to a different repository")
)

// ActiveRunError means a new prompt arrived while a run was still active.
// The prompt was not stored and the active run's goal was not changed.
type ActiveRunError struct {
	RunID string
}

func (e *ActiveRunError) Error() string {
	return "a run is already active (" + e.RunID + "). This prompt was not stored and the run goal was not changed"
}

func (e *ActiveRunError) Is(target error) bool { return target == ErrSessionBusy }

// RepoMismatchError means a caller named a different repository for an existing session.
// The stored path is left unchanged.
type RepoMismatchError struct {
	Bound string
	Asked string
}

func (e *RepoMismatchError) Error() string {
	return "session is bound to " + e.Bound + " and was not redirected to " + e.Asked
}

func (e *RepoMismatchError) Is(target error) bool { return target == ErrRepoMismatch }
