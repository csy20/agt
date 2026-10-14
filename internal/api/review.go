package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/csy20/agt/internal/patch"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/tools"
	"github.com/csy20/agt/internal/workspace"
)

func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	current, art, work, base, ok := s.review(w, r)
	if !ok {
		return
	}
	text, err := tools.Diff(base, work)
	complete := true
	note := ""
	if err != nil {
		if strings.Contains(err.Error(), "exceeds") {
			complete = false
			note = err.Error() + "; export the patch artifact for the complete bytes"
			text = ""
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":     current.ID,
		"repo_path":  current.RepoPath,
		"diff":       text,
		"digest":     art.Digest,
		"complete":   complete,
		"note":       note,
		"strategy":   patch.Strategy,
		"revision":   art.Revision,
		"file_count": len(art.Files),
	})
}

func (s *Server) patchArtifact(w http.ResponseWriter, r *http.Request) {
	_, art, _, _, ok := s.review(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, art)
}

func (s *Server) apply(w http.ResponseWriter, r *http.Request) {
	current, err := s.Store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if current.State != run.Succeeded {
		writeError(w, http.StatusConflict, "apply requires SUCCEEDED")
		return
	}
	var req struct {
		Digest string `json:"digest"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if strings.TrimSpace(req.Digest) == "" {
		writeError(w, http.StatusBadRequest, "digest is required")
		return
	}
	art, _, _, err := s.artifact(current)
	if err != nil {
		writeReviewError(w, err)
		return
	}
	if req.Digest != art.Digest || art.RepoPath != current.RepoPath || art.RunID != current.ID {
		writeError(w, http.StatusConflict, "digest does not match the run artifact")
		return
	}
	if s.Config.DataDir == "" {
		writeError(w, http.StatusInternalServerError, "data directory is not configured")
		return
	}
	lockPath := filepath.Join(s.Config.DataDir, "runs", current.ID, "apply.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		writeError(w, http.StatusConflict, "an apply for this run is already in progress or was interrupted; remove apply.lock after checking the rollback directory")
		return
	}
	rollback := filepath.Join(s.Config.DataDir, "runs", current.ID, "rollback", time.Now().UTC().Format("20060102T150405.000000000Z"))
	_, _ = lock.WriteString(rollback + "\n")
	_ = lock.Close()
	defer os.Remove(lockPath)
	if err := patch.Apply(current.RepoPath, art, rollback); err != nil {
		writeReviewError(w, err)
		return
	}
	_, _ = s.Store.AppendEvent(r.Context(), current.ID, "patch.applied", "user", "local", map[string]any{
		"digest":    art.Digest,
		"repo_path": current.RepoPath,
		"files":     len(art.Files),
		"rollback":  rollback,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"applied":   len(art.Files),
		"digest":    art.Digest,
		"repo_path": current.RepoPath,
		"rollback":  rollback,
	})
}

func (s *Server) review(w http.ResponseWriter, r *http.Request) (store.Run, patch.Artifact, string, string, bool) {
	current, err := s.Store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return store.Run{}, patch.Artifact{}, "", "", false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return store.Run{}, patch.Artifact{}, "", "", false
	}
	art, work, base, err := s.artifact(current)
	if err != nil {
		writeReviewError(w, err)
		return store.Run{}, patch.Artifact{}, "", "", false
	}
	return current, art, work, base, true
}

func (s *Server) artifact(current store.Run) (patch.Artifact, string, string, error) {
	work, base, err := s.snapshot(current)
	if err != nil {
		return patch.Artifact{}, "", "", err
	}
	captured := workspace.ReadCapture(current.RepoPath)
	art, err := patch.Build(base, work, patch.Meta{
		RunID:    current.ID,
		RepoPath: current.RepoPath,
		Revision: captured.Revision,
	})
	if err != nil {
		return patch.Artifact{}, "", "", err
	}
	return art, work, base, nil
}

func (s *Server) snapshot(current store.Run) (string, string, error) {
	if current.Worktree == "" {
		return "", "", errNoWorkspace
	}
	if s.Config.DataDir == "" {
		return "", "", fmt.Errorf("data directory is not configured")
	}
	if strings.Contains(current.ID, "/") || strings.Contains(current.ID, "\\") || current.ID == "" {
		return "", "", fmt.Errorf("run id is not a snapshot name")
	}
	want := filepath.Clean(filepath.Join(s.Config.DataDir, "runs", current.ID, "work"))
	got := filepath.Clean(current.Worktree)
	if want != got {
		return "", "", fmt.Errorf("worktree is not the run snapshot")
	}
	base := filepath.Join(filepath.Dir(got), "base")
	if _, err := os.Stat(got); err != nil {
		return "", "", fmt.Errorf("snapshot is not available")
	}
	if _, err := os.Stat(base); err != nil {
		return "", "", fmt.Errorf("snapshot is not available")
	}
	return got, base, nil
}

var errNoWorkspace = errors.New("workspace has not been captured")

func writeReviewError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNoWorkspace):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, patch.ErrDigest), errors.Is(err, patch.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, patch.ErrEscape):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		if strings.Contains(err.Error(), "restored") {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
