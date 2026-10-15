package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
)

func (s *Server) pause(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, run.Paused)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	s.transition(w, r, run.Cancelled)
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, to run.State) {
	current, err := s.Store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.Transition(r.Context(), current.ID, current.StateVersion, to, "api")
	if err != nil {
		writeTransitionError(w, err)
		return
	}
	s.writeRun(w, r, updated)
}

func (s *Server) resume(w http.ResponseWriter, r *http.Request) {
	s.resumeFrom(w, r, run.Paused, nil)
}

func (s *Server) budgetResume(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BudgetCents *int `json:"budget_cents"`
	}
	if err := decodeOptional(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	s.resumeFrom(w, r, run.PausedBudget, req.BudgetCents)
}

func (s *Server) resumeFrom(w http.ResponseWriter, r *http.Request, from run.State, budget *int) {
	current, err := s.Store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, err := s.Store.Resume(r.Context(), current.ID, current.StateVersion, from, budget)
	if err != nil {
		writeTransitionError(w, err)
		return
	}
	s.writeRun(w, r, updated)
}

func (s *Server) cleanup(w http.ResponseWriter, r *http.Request) {
	if s.Cleanup == nil {
		writeError(w, http.StatusNotImplemented, "cleanup is not configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	n, err := s.Cleanup(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": n})
}

func (s *Server) writeRun(w http.ResponseWriter, r *http.Request, current store.Run) {
	view, err := s.present(r.Context(), current)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func writeTransitionError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if errors.Is(err, store.ErrConflict) || strings.Contains(err.Error(), "illegal transition") {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func decodeOptional(r *http.Request, dest any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(dest)
}
