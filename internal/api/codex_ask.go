package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/csy20/agt/internal/agent"
	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/workspace"
)

func (s *Server) codexAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath  string `json:"repo_path"`
		Prompt    string `json:"prompt"`
		Authorize bool   `json:"authorize"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !req.Authorize {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":        "live Codex ask requires authorize. A saved login is not permission.",
			"label":        "blocked",
			"live":         false,
			"turn_started": false,
			"prompt_sent":  false,
		})
		return
	}
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if err := s.Store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if s.Config.DataDir == "" {
		writeError(w, http.StatusBadRequest, "data dir is not set")
		return
	}
	repo, err := workspace.CanonicalRepo(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := workspace.Overlap(repo, s.Config.DataDir); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	runID := id.New("codex_")
	work, _, err := workspace.Materialize(repo, s.Config.DataDir, runID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	defer os.RemoveAll(filepath.Join(s.Config.DataDir, "runs", runID))
	result, err := agent.Ask(r.Context(), agent.AskRequest{
		Command:   s.CodexCommand,
		WorkDir:   work,
		Prompt:    req.Prompt,
		Excerpts:  codexExcerpts(work),
		Authorize: true,
	})
	status := http.StatusOK
	if err != nil {
		status = http.StatusConflict
		if errors.Is(err, agent.ErrLogin) {
			status = http.StatusPreconditionFailed
		}
		if errors.Is(err, agent.ErrLiveUnavailable) || errors.Is(err, agent.ErrMutationUnavailable) {
			status = http.StatusForbidden
		}
	}
	body := map[string]any{
		"label":        result.Label,
		"live":         result.Live,
		"turn_started": result.TurnStarted,
		"prompt_sent":  result.PromptSent,
		"result":       result,
	}
	if err != nil {
		body["error"] = err.Error()
	}
	writeJSON(w, status, body)
}

func codexExcerpts(work string) []string {
	var out []string
	for _, rel := range []string{"payments/processor.py", "tests/test_retry.py"} {
		path := filepath.Join(work, filepath.FromSlash(rel))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		out = append(out, rel)
	}
	return out
}
