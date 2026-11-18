package server

import (
	"net/http"
	"testing"
)

// Parity-exempt: assembly guard, not an upstream behaviour. A duplicate
// ServeMux pattern panics at registration time, so this pins that the
// colony-worker table can be registered next to the pre-existing tables
// before the integrator wires it into NewApp.
func TestColonyWorkerRoutesRegisterAlongsideExisting(t *testing.T) {
	mux := http.NewServeMux()
	RegisterConfigRoutes(mux, ConfigRouteDeps{})
	RegisterSessionRoutes(mux, SessionRouteDeps{})
	RegisterAgentRoutes(mux, AgentRouteDeps{})
	RegisterChatRoutes(mux, ChatRouteDeps{})
	RegisterColonyWorkerRoutes(mux, ColonyWorkerDeps{})
}
