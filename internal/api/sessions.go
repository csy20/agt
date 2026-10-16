package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/workspace"
)

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RepoPath string `json:"repo_path"`
		Title    string `json:"title"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	repo, err := workspace.CanonicalRepo(req.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.Config.DataDir != "" {
		if err := workspace.Overlap(repo, s.Config.DataDir); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	sess, err := s.Store.CreateSession(r.Context(), repo, req.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Copy the daemon selection now. A later change to the default file does not move this session.
	name := s.selectedName()
	if err := s.Store.SetSessionModel(r.Context(), sess.ID, name); err != nil {
		writeError(w, http.StatusInternalServerError, s.hide(err.Error()))
		return
	}
	sess.ModelConfig = name
	writeJSON(w, http.StatusCreated, map[string]any{"session": sessionJSON(sess)})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	repo, err := workspace.CanonicalRepo(r.URL.Query().Get("repo_path"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "limit must be from 1 to 100")
			return
		}
		limit = n
	}
	sessions, err := s.Store.ListSessions(r.Context(), repo, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := []any{}
	for _, sess := range sessions {
		out = append(out, sessionJSON(sess))
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func (s *Server) readSession(w http.ResponseWriter, r *http.Request) {
	s.writeSession(w, r, false)
}

func (s *Server) openSession(w http.ResponseWriter, r *http.Request) {
	s.writeSession(w, r, true)
}

func (s *Server) writeSession(w http.ResponseWriter, r *http.Request, opening bool) {
	var asked string
	if opening {
		var req struct {
			RepoPath string `json:"repo_path"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}
		repo, err := workspace.CanonicalRepo(req.RepoPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		asked = repo
	} else if raw := r.URL.Query().Get("repo_path"); raw != "" {
		repo, err := workspace.CanonicalRepo(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		asked = repo
	}
	sess, err := s.Store.GetSession(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if asked != "" && asked != sess.RepoPath {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     (&store.RepoMismatchError{Bound: sess.RepoPath, Asked: asked}).Error(),
			"repo_path": sess.RepoPath,
		})
		return
	}
	view, err := s.sessionView(r, sess)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) submitSession(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	var extra struct {
		ClientMessageID string `json:"client_message_id"`
		Content         string `json:"content"`
	}
	_ = json.Unmarshal(body, &extra)
	if !store.ValidClientMessageID(extra.ClientMessageID) {
		writeError(w, http.StatusBadRequest, "client_message_id is required")
		return
	}
	sess, err := s.Store.GetSession(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, s.hide(err.Error()))
		return
	}
	in, code, msg := s.prepareRun(body, sess.ModelConfig)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	repo, err := workspace.CanonicalRepo(in.RepoPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if repo != sess.RepoPath {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":     (&store.RepoMismatchError{Bound: sess.RepoPath, Asked: repo}).Error(),
			"repo_path": sess.RepoPath,
		})
		return
	}
	in.RepoPath = sess.RepoPath
	in.RequestHash = requestHash(in.Goal, in.RepoPath, in.BudgetCents, in.MaxSteps, in.WallSeconds, in.Provider, in.ModelConfig, in.ModelName, in.ScriptJSON, in.ProfileJSON, in.Interaction, in.TaskType, in.ContractJSON)
	pending, err := s.pendingWithDigests(r, sess.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	content := extra.Content
	if content == "" {
		content = in.Goal
	}
	result, err := s.Store.Submit(r.Context(), store.SubmitInput{
		SessionID: sess.ID, ClientMessageID: extra.ClientMessageID, Content: content,
		Create: in, Pending: pending,
	})
	if err != nil {
		var busy *store.ActiveRunError
		if errors.As(err, &busy) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": busy.Error(), "active_run_id": busy.RunID, "session_id": sess.ID,
			})
			return
		}
		var mismatch *store.RepoMismatchError
		if errors.As(err, &mismatch) {
			writeJSON(w, http.StatusConflict, map[string]any{
				"error": mismatch.Error(), "repo_path": mismatch.Bound,
			})
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	notice := ""
	if len(result.Pending) > 0 {
		notice = "A previous patch is still pending. Its edits are absent from this snapshot. It was not applied and it was not discarded."
	}
	writeJSON(w, status, map[string]any{
		"session_id":        sess.ID,
		"run_id":            result.Run.ID,
		"state":             result.Run.State,
		"created":           result.Created,
		"client_message_id": result.Message.ClientMessageID,
		"pending":           pendingJSON(result.Pending),
		"notice":            notice,
		"events_url":        "/v1/runs/" + result.Run.ID + "/events",
	})
}

func (s *Server) sessionView(r *http.Request, sess store.Session) (map[string]any, error) {
	msgs, err := s.Store.ListSessionMessages(r.Context(), sess.ID)
	if err != nil {
		return nil, err
	}
	runs, err := s.Store.ListSessionRuns(r.Context(), sess.ID)
	if err != nil {
		return nil, err
	}
	views := []runViewBody{}
	active := ""
	for _, current := range runs {
		view, err := s.present(r.Context(), current)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
		if !run.IsTerminal(current.State) {
			active = current.ID
		}
	}
	pending, err := s.pendingWithDigests(r, sess.ID)
	if err != nil {
		return nil, err
	}
	messages := []any{}
	for _, msg := range msgs {
		messages = append(messages, map[string]any{
			"seq": msg.Seq, "client_message_id": msg.ClientMessageID,
			"role": msg.Role, "kind": msg.Kind, "content": msg.Content,
			"run_id": msg.RunID, "created_at": msg.CreatedAt,
		})
	}
	return map[string]any{
		"session":       sessionJSON(sess),
		"messages":      messages,
		"runs":          views,
		"active_run_id": active,
		"pending":       pendingJSON(pending),
	}, nil
}

func (s *Server) pendingWithDigests(r *http.Request, sessionID string) ([]store.PendingPatch, error) {
	pending, err := s.Store.PendingPatches(r.Context(), sessionID)
	if err != nil {
		return nil, err
	}
	for i := range pending {
		current, err := s.Store.GetRun(r.Context(), pending[i].RunID)
		if err != nil {
			return nil, err
		}
		if art, _, _, err := s.artifact(current); err == nil {
			pending[i].Digest = art.Digest
			pending[i].Note = store.PendingNote(pending[i])
		}
	}
	return pending, nil
}

func sessionJSON(sess store.Session) map[string]any {
	return map[string]any{
		"id": sess.ID, "repo_path": sess.RepoPath, "title": sess.Title,
		"model_config": sess.ModelConfig,
		"created_at":   sess.CreatedAt, "updated_at": sess.UpdatedAt,
	}
}

func pendingJSON(pending []store.PendingPatch) []any {
	out := []any{}
	for _, patch := range pending {
		note := patch.Note
		if note == "" {
			note = store.PendingNote(patch)
		}
		out = append(out, map[string]any{
			"run_id": patch.RunID, "digest": patch.Digest, "outcome": patch.Outcome,
			"change": patch.Change, "summary": patch.Summary, "note": note,
		})
	}
	return out
}
