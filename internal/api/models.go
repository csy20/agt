package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/csy20/agt/internal/config"
	"github.com/csy20/agt/internal/store"
	"github.com/csy20/agt/internal/workspace"
)

func (s *Server) catalog() config.Catalog {
	return config.CatalogFrom(s.Config)
}

func (s *Server) selectedName() string {
	name := config.ReadSelectedModel(s.Config.DataDir)
	if name == "" {
		return config.DefaultModelName
	}
	return name
}

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	selected := s.selectedName()
	writeJSON(w, http.StatusOK, map[string]any{
		"selected": selected,
		"configs":  s.catalog().Public(selected, s.getenv),
	})
}

func (s *Server) selectModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	resolved, err := s.catalog().Resolve(strings.TrimSpace(req.Name))
	if err != nil {
		writeError(w, http.StatusBadRequest, s.hide(err.Error()))
		return
	}
	if err := config.WriteSelectedModel(s.Config.DataDir, resolved.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"selected": resolved.Name,
		"config":   s.publicOne(resolved.Name),
		"notice":   "No inference request was made.",
	})
}

func (s *Server) selectSessionModel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name     string `json:"name"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
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
	if strings.TrimSpace(req.RepoPath) != "" {
		repo, err := workspace.CanonicalRepo(req.RepoPath)
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
	}
	resolved, err := s.catalog().Resolve(strings.TrimSpace(req.Name))
	if err != nil {
		writeError(w, http.StatusBadRequest, s.hide(err.Error()))
		return
	}
	if err := s.Store.SetSessionModel(r.Context(), sess.ID, resolved.Name); err != nil {
		writeError(w, http.StatusInternalServerError, s.hide(err.Error()))
		return
	}
	sess.ModelConfig = resolved.Name
	writeJSON(w, http.StatusOK, map[string]any{
		"session": sessionJSON(sess),
		"config":  s.publicOne(resolved.Name),
		"notice":  "No inference request was made.",
	})
}

func (s *Server) publicOne(name string) config.PublicModel {
	cat := s.catalog()
	items := cat.Public(name, s.getenv)
	for i, cfg := range cat.Configs {
		if cfg.Name == name && i < len(items) {
			return items[i]
		}
	}
	return config.PublicModel{Name: name}
}

func (s *Server) getenv(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if key == "AGT_MODEL_API_KEY" {
		return s.Config.ModelAPIKey
	}
	return ""
}

func (s *Server) hide(text string) string {
	return s.catalog().Hide(text, s.getenv)
}

func (s *Server) applyResolved(in *store.CreateInput, resolved config.Resolved, scriptFromClient bool) {
	in.Provider = resolved.Adapter
	in.ModelConfig = resolved.Name
	in.ModelName = resolved.Model
	in.ModelEndpoint = resolved.Endpoint
	in.InputPerMillion = resolved.InputPerMillion
	in.OutputPerMillion = resolved.OutputPerMillion
	in.PricingRecorded = true
	in.CredentialEnv = resolved.CredentialEnv
	if !scriptFromClient && resolved.ScriptPath != "" && in.Provider == "fake" {
		in.ScriptPath = resolved.ScriptPath
	}
}

func (s *Server) resolveForCreate(explicit, provider, preferred string) (config.Resolved, string, error) {
	cat := s.catalog()
	explicit = strings.TrimSpace(explicit)
	provider = strings.TrimSpace(provider)
	if explicit != "" {
		resolved, err := cat.Resolve(explicit)
		if err != nil {
			return config.Resolved{}, "", err
		}
		if provider != "" && provider != resolved.Adapter {
			return config.Resolved{}, "", errProviderMismatch
		}
		return resolved, resolved.Adapter, nil
	}
	name := strings.TrimSpace(preferred)
	if name == "" {
		name = s.selectedName()
	}
	if provider == "" {
		resolved, err := cat.Resolve(name)
		if err != nil {
			return config.Resolved{}, "", err
		}
		return resolved, resolved.Adapter, nil
	}
	selected, selErr := cat.Resolve(name)
	if selErr == nil && selected.Adapter == provider {
		return selected, provider, nil
	}
	if selErr != nil {
		if cfg, ok := lookupAdapter(cat, name); ok && cfg == provider {
			return config.Resolved{}, "", selErr
		}
	}
	resolved, err := cat.Legacy(provider)
	if err != nil {
		return config.Resolved{}, "", err
	}
	return resolved, provider, nil
}

func lookupAdapter(cat config.Catalog, name string) (string, bool) {
	for _, cfg := range cat.Configs {
		if cfg.Name == name {
			return cfg.Adapter, true
		}
	}
	return "", false
}

var errProviderMismatch = errors.New("provider does not match the selected model configuration")

func forbiddenModelFields(body []byte) string {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return ""
	}
	for _, key := range []string{"endpoint", "api_key", "model_api_key", "model_base_url", "credential"} {
		if _, ok := top[key]; ok {
			return key + " is not accepted. Choose a named model configuration"
		}
	}
	return ""
}
