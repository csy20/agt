package api

import (
	"net/http"

	"github.com/csy20/agt/internal/version"
)

func (s *Server) stamp(w http.ResponseWriter) {
	w.Header().Set(version.ProductHeader, version.Name)
	w.Header().Set(version.VersionHeader, version.Product)
	w.Header().Set(version.ContractHeader, version.Contract)
	w.Header().Set(version.BuildHeader, version.BuildID())
	w.Header().Set(version.CapabilitiesHeader, version.CapabilityHeaderValue())
}

func (s *Server) identity(w http.ResponseWriter, r *http.Request) {
	// Product, version, provider, and model name are not secrets. The API key
	// and the bearer token are never written here. Selection does not call a model.
	selected := s.selectedName()
	resolved, err := s.catalog().Resolve(selected)
	provider := s.Config.Provider
	modelName := s.Config.ModelName
	scripted := provider == "fake" || provider == ""
	configName := selected
	if err == nil {
		provider = resolved.Adapter
		modelName = resolved.Model
		scripted = resolved.Scripted
		configName = resolved.Name
	}
	modelName = s.hide(modelName)
	writeJSON(w, http.StatusOK, map[string]any{
		"product":      version.Name,
		"version":      version.Product,
		"contract":     version.Contract,
		"build":        version.BuildID(),
		"capabilities": version.Capabilities(),
		"provider":     provider,
		"model":        modelName,
		"model_config": configName,
		"scripted":     scripted,
		"listen":       s.Config.ListenAddr,
	})
}
