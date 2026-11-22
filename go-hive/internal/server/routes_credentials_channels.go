package server

// Parity: framework/server/channels.py::register

import (
	"net/http"
	"sort"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

type ChannelRouteDeps struct {
	Home   hivehome.Home
	States func() map[string]map[string]any
}

func RegisterChannelRoutes(mux *http.ServeMux, deps ChannelRouteDeps) {
	mux.HandleFunc("GET /api/channels", func(w http.ResponseWriter, r *http.Request) {
		handleListChannels(w, deps)
	})
}

// Parity: framework/server/channels.py::handle_list_channels
func handleListChannels(w http.ResponseWriter, deps ChannelRouteDeps) {
	states := map[string]map[string]any{}
	if deps.States != nil {
		states = deps.States()
	}
	names := make([]string, 0, len(states))
	for name := range states {
		names = append(names, name)
	}
	sort.Strings(names)
	channels := make([]any, 0, len(names))
	for _, name := range names {
		entry := map[string]any{"name": name}
		for key, value := range states[name] {
			entry[key] = value
		}
		channels = append(channels, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}
