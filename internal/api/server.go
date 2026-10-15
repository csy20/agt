package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/id"
	"github.com/csy20/agt/internal/model"
	"github.com/csy20/agt/internal/policy"
	"github.com/csy20/agt/internal/profile"
	"github.com/csy20/agt/internal/run"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/version"
	"github.com/csy20/agt/internal/workspace"
)

type Server struct {
	Store  *store.Store
	Config config.Config
	Token  string
	// Cleanup removes abandoned sandbox containers. The API does not import
	// the sandbox package; the daemon injects this.
	Cleanup func(context.Context) (int, error)
}

func New(st *store.Store, cfg config.Config, token string) *Server {
	return &Server{Store: st, Config: cfg, Token: token}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /v1/identity", s.identity)
	mux.HandleFunc("POST /v1/runs", s.createRun)
	mux.HandleFunc("GET /v1/runs", s.listRuns)
	mux.HandleFunc("GET /v1/runs/{id}", s.getRun)
	mux.HandleFunc("GET /v1/runs/{id}/events", s.events)
	mux.HandleFunc("GET /v1/runs/{id}/commands", s.listCommands)
	mux.HandleFunc("GET /v1/runs/{id}/commands/{command_id}", s.getCommand)
	mux.HandleFunc("POST /v1/runs/{id}/approvals", s.approve)
	mux.HandleFunc("GET /v1/runs/{id}/diff", s.diff)
	mux.HandleFunc("GET /v1/runs/{id}/patch", s.patchArtifact)
	mux.HandleFunc("POST /v1/runs/{id}/apply", s.apply)
	mux.HandleFunc("POST /v1/runs/{id}/pause", s.pause)
	mux.HandleFunc("POST /v1/runs/{id}/resume", s.resume)
	mux.HandleFunc("POST /v1/runs/{id}/budget-resume", s.budgetResume)
	mux.HandleFunc("POST /v1/runs/{id}/cancel", s.cancel)
	mux.HandleFunc("POST /v1/cleanup", s.cleanup)
	mux.HandleFunc("POST /v1/sessions", s.createSession)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)
	mux.HandleFunc("GET /v1/sessions/{id}", s.readSession)
	mux.HandleFunc("POST /v1/sessions/{id}/open", s.openSession)
	mux.HandleFunc("POST /v1/sessions/{id}/messages", s.submitSession)
	return s.guard(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	s.stamp(w)
	if s.Store == nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := s.Store.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeError(w, http.StatusBadRequest, "limit must be from 1 to 100")
			return
		}
		limit = n
	}
	if s.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	runs, err := s.Store.ListRuns(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := []runViewBody{}
	for _, current := range runs {
		view, err := s.present(r.Context(), current)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": views})
}

type createRequest struct {
	Goal        string          `json:"goal"`
	RepoPath    string          `json:"repo_path"`
	BudgetCents *int            `json:"budget_cents"`
	MaxSteps    *int            `json:"max_steps"`
	WallSeconds *int            `json:"wall_seconds"`
	Provider    string          `json:"provider"`
	ScriptPath  string          `json:"script_path"`
	Profile     json.RawMessage `json:"profile"`
	// Interaction is agent, ask, or plan. Empty means agent.
	// ask and plan are read-only. PLANNING remains the normal repair state.
	Interaction string `json:"interaction"`
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	in, code, msg := s.prepareRun(body)
	if code != 0 {
		writeError(w, code, msg)
		return
	}
	in.IdempotencyKey = r.Header.Get("Idempotency-Key")
	created, isNew, err := s.Store.CreateRun(r.Context(), in)
	if errors.Is(err, store.ErrIdempotency) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusAccepted
	if !isNew {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"run_id":     created.ID,
		"state":      created.State,
		"events_url": "/v1/runs/" + created.ID + "/events",
		"created":    isNew,
	})
}

// prepareRun validates a create body. A zero status means the input is ready.
// Idempotency and session linkage are applied by the caller.
func (s *Server) prepareRun(body []byte) (store.CreateInput, int, string) {
	if imageField(body) {
		return store.CreateInput{}, http.StatusBadRequest, "image is not accepted; the daemon selects the profile image"
	}
	var req createRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return store.CreateInput{}, http.StatusBadRequest, "invalid JSON"
	}
	if strings.TrimSpace(req.Goal) == "" {
		return store.CreateInput{}, http.StatusBadRequest, "goal is required"
	}
	repo, err := filepath.Abs(req.RepoPath)
	if err != nil {
		return store.CreateInput{}, http.StatusBadRequest, "repo_path"
	}
	info, err := os.Stat(repo)
	if err != nil || !info.IsDir() {
		return store.CreateInput{}, http.StatusBadRequest, "repo_path is not a directory"
	}
	if s.Config.DataDir != "" {
		if err := workspace.Overlap(repo, s.Config.DataDir); err != nil {
			return store.CreateInput{}, http.StatusBadRequest, err.Error()
		}
	}
	provider := req.Provider
	if provider == "" {
		provider = s.Config.Provider
	}
	if provider != "fake" && provider != "openai" {
		return store.CreateInput{}, http.StatusBadRequest, "provider must be fake or openai"
	}
	budget := 100
	if req.BudgetCents != nil {
		budget = *req.BudgetCents
	}
	if budget < 0 || budget > 100_000 {
		return store.CreateInput{}, http.StatusBadRequest, "budget_cents out of range"
	}
	maxSteps := 16
	if req.MaxSteps != nil {
		maxSteps = *req.MaxSteps
	}
	if maxSteps < 0 || maxSteps > 100 {
		return store.CreateInput{}, http.StatusBadRequest, "max_steps out of range"
	}
	wall := 300
	if req.WallSeconds != nil {
		wall = *req.WallSeconds
	}
	if wall <= 0 || wall > 3600 {
		return store.CreateInput{}, http.StatusBadRequest, "wall_seconds out of range"
	}
	var script json.RawMessage
	scriptPath := ""
	if provider == "fake" {
		if req.ScriptPath == "" {
			return store.CreateInput{}, http.StatusBadRequest, "fake provider requires script_path"
		}
		scriptPath, err = filepath.Abs(req.ScriptPath)
		if err != nil {
			return store.CreateInput{}, http.StatusBadRequest, "script_path"
		}
		st, err := os.Lstat(scriptPath)
		if err != nil || !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
			return store.CreateInput{}, http.StatusBadRequest, "script_path must be a regular file"
		}
		if st.Size() > 1<<20 {
			return store.CreateInput{}, http.StatusBadRequest, "script_path is too large"
		}
		raw, err := os.ReadFile(scriptPath)
		if err != nil {
			return store.CreateInput{}, http.StatusBadRequest, "script_path unreadable"
		}
		parsed, err := model.ParseScript(raw)
		if err != nil {
			return store.CreateInput{}, http.StatusBadRequest, "script is invalid: " + err.Error()
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			return store.CreateInput{}, http.StatusBadRequest, "script is invalid"
		}
		script = encoded
	}
	if provider == "openai" && (s.Config.ModelName == "" || (s.Config.ModelAPIKey == "" && !model.LoopbackURL(s.Config.ModelBaseURL))) {
		return store.CreateInput{}, http.StatusBadRequest, "AGT_MODEL_NAME is required for the openai provider. AGT_MODEL_API_KEY is required unless AGT_MODEL_BASE_URL is a loopback URL"
	}
	profReq, err := decodeProfile(req.Profile)
	if err != nil {
		return store.CreateInput{}, http.StatusBadRequest, err.Error()
	}
	images := s.Config.ProfileImages()
	resolved, err := profile.Resolve(profReq, images, runtime.GOARCH)
	if err != nil {
		return store.CreateInput{}, http.StatusBadRequest, err.Error()
	}
	profileJSON, err := resolved.Canonical()
	if err != nil {
		return store.CreateInput{}, http.StatusBadRequest, "profile"
	}
	interaction := policy.NormalizeInteraction(req.Interaction)
	if !policy.ValidInteraction(interaction) {
		return store.CreateInput{}, http.StatusBadRequest, "interaction must be agent, ask, or plan"
	}
	hash := requestHash(req.Goal, repo, budget, maxSteps, wall, provider, script, profileJSON, interaction)
	return store.CreateInput{
		RequestHash:  hash,
		Goal:         req.Goal,
		RepoPath:     repo,
		BudgetCents:  budget,
		MaxSteps:     maxSteps,
		WallSeconds:  wall,
		AgentVersion: version.Compute(images.Python, images.Go, images.Node),
		Provider:     provider,
		ScriptPath:   scriptPath,
		ScriptJSON:   script,
		ProfileJSON:  profileJSON,
		Interaction:  interaction,
	}, 0, ""
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	current, err := s.Store.GetRun(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	view, err := s.present(r.Context(), current)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("id")
	if _, err := s.Store.GetRun(r.Context(), runID); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "run not found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("stream") == "1" || strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		s.stream(w, r, runID, after)
		return
	}
	evs, err := s.Store.EventsAfter(r.Context(), runID, after)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, evs)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, runID string, after int64) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	s.stamp(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	idleTicks := 0
	for {
		if r.Context().Err() != nil {
			return
		}
		evs, err := s.Store.EventsAfter(r.Context(), runID, after)
		if err != nil {
			return
		}
		if len(evs) == 0 {
			idleTicks++
			if idleTicks >= heartbeatEvery {
				idleTicks = 0
				fmt.Fprintf(w, ": heartbeat\n\n")
				flusher.Flush()
			}
		} else {
			idleTicks = 0
			for _, ev := range evs {
				fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, ev.Payload)
				after = ev.Seq
			}
			flusher.Flush()
		}
		current, err := s.Store.GetRun(r.Context(), runID)
		if err != nil {
			return
		}
		if stopStream(current.State) {
			more, err := s.Store.EventsAfter(r.Context(), runID, after)
			if err != nil || len(more) == 0 {
				return
			}
			continue
		}
		timer := time.NewTimer(streamPoll)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

const (
	streamPoll     = 150 * time.Millisecond
	heartbeatEvery = 100 // about 15s at streamPoll, so a client can notice a dead connection
)

func stopStream(state run.State) bool {
	return run.IsTerminal(state) || state == run.Paused || state == run.PausedBudget || state == run.AwaitingApproval
}

func (s *Server) listCommands(w http.ResponseWriter, r *http.Request) {
	cmds, err := s.Store.ListCommands(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cmds)
}

func (s *Server) getCommand(w http.ResponseWriter, r *http.Request) {
	cmd, err := s.Store.GetCommand(r.Context(), r.PathValue("id"), r.PathValue("command_id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "command not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cmd)
}

type approvalRequest struct {
	CommandID string `json:"command_id"`
	ArgsHash  string `json:"args_hash"`
	Approver  string `json:"approver"`
	Decision  string `json:"decision"`
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var req approvalRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Approver == "" {
		req.Approver = "local"
	}
	if req.Decision == "" {
		req.Decision = "approved"
	}
	current, err := s.Store.Approve(r.Context(), store.Approval{
		ID:        id.New("appr_"),
		RunID:     r.PathValue("id"),
		CommandID: req.CommandID,
		ArgsHash:  req.ArgsHash,
		Approver:  req.Approver,
		Decision:  req.Decision,
	})
	if errors.Is(err, store.ErrHashMismatch) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "command not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	view, err := s.present(r.Context(), current)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func requestHash(goal, repo string, budget, steps, wall int, provider string, script, profileJSON []byte, interaction string) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n%s\n%d\n%d\n%d\n%s\n%s\n", goal, repo, budget, steps, wall, provider, interaction)
	sum.Write(script)
	sum.Write([]byte("\n"))
	sum.Write(profileJSON)
	return hex.EncodeToString(sum.Sum(nil))
}

func imageField(body []byte) bool {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return false
	}
	if _, ok := top["image"]; ok {
		return true
	}
	raw, ok := top["profile"]
	if !ok {
		return false
	}
	var prof map[string]json.RawMessage
	if err := json.Unmarshal(raw, &prof); err != nil {
		return false
	}
	_, ok = prof["image"]
	return ok
}

func decodeProfile(raw json.RawMessage) (profile.Request, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return profile.Request{}, fmt.Errorf("profile is required")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var req profile.Request
	if err := dec.Decode(&req); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return profile.Request{}, fmt.Errorf("profile only accepts name, argv, workdir, timeout_seconds, memory, pids, and prepare")
		}
		return profile.Request{}, fmt.Errorf("profile: %s", err.Error())
	}
	return req, nil
}

type runViewBody struct {
	ID           string          `json:"id"`
	Goal         string          `json:"goal"`
	RepoPath     string          `json:"repo_path"`
	Worktree     string          `json:"worktree,omitempty"`
	State        run.State       `json:"state"`
	StateVersion int64           `json:"state_version"`
	BudgetCents  int             `json:"budget_cents"`
	SpentMicros  int64           `json:"spent_micros"`
	AgentVersion string          `json:"agent_version"`
	TraceID      string          `json:"trace_id"`
	Provider     string          `json:"provider"`
	StepCount    int             `json:"step_count"`
	MaxSteps     int             `json:"max_steps"`
	UsageMicros  int64           `json:"usage_micros"`
	Error        string          `json:"error,omitempty"`
	Interaction  string          `json:"interaction,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	Plan         json.RawMessage `json:"plan,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	Profile      json.RawMessage `json:"profile,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

func (s *Server) present(ctx context.Context, current store.Run) (runViewBody, error) {
	view := runView(current)
	total, err := s.Store.UsageTotal(ctx, current.ID)
	if err != nil {
		return runViewBody{}, err
	}
	view.UsageMicros = total
	return view, nil
}

func runView(current store.Run) runViewBody {
	view := runViewBody{
		ID: current.ID, Goal: current.Goal, RepoPath: current.RepoPath, Worktree: current.Worktree,
		State: current.State, StateVersion: current.StateVersion, BudgetCents: current.BudgetCents,
		SpentMicros: current.SpentMicros, AgentVersion: current.AgentVersion, TraceID: current.TraceID,
		Provider: current.Provider, StepCount: current.StepCount, MaxSteps: current.MaxSteps,
		Error: current.Error, Interaction: current.Interaction, SessionID: current.SessionID,
		CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
	}
	if len(current.Plan) > 0 && string(current.Plan) != "null" {
		view.Plan = current.Plan
	}
	if len(current.Result) > 0 && string(current.Result) != "null" {
		view.Result = current.Result
	}
	if parsed, err := profile.Parse(current.ProfileJSON); err == nil {
		raw, err := json.Marshal(parsed.Identity())
		if err == nil {
			view.Profile = raw
		}
	}
	return view
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
