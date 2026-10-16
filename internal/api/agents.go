package api

import (
	"net/http"

	"github.com/csy20/agt/internal/agent"
)

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	probe := s.AgentProbe
	if probe == nil {
		probe = agent.DefaultVersion
	}
	selected := s.selectedName()
	screen := agent.BuildScreen(s.catalog().Public(selected, s.getenv), agent.Probe(probe))
	writeJSON(w, http.StatusOK, screen)
}
