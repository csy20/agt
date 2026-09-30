package store

import "errors"

var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrIdempotency  = errors.New("idempotency key reused with a different request")
	ErrHashMismatch = errors.New("approval does not match the canonical command")
)
