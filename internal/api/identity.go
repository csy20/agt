package api

import (
	"net/http"

	"github.com/csy20/agt/internal/version"
)

func (s *Server) stamp(w http.ResponseWriter) {
	w.Header().Set(version.ProductHeader, version.Name)
	w.Header().Set(version.VersionHeader, version.Product)
}

func (s *Server) identity(w http.ResponseWriter, r *http.Request) {
	// Product, version, provider, and model name are not secrets. The API key
	// and the bearer token are never written here.
	writeJSON(w, http.StatusOK, map[string]any{
		"product":  version.Name,
		"version":  version.Product,
		"provider": s.Config.Provider,
		"model":    s.Config.ModelName,
		"listen":   s.Config.ListenAddr,
	})
}
