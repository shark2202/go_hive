package cli

// Serve-time channel host assembly. Kept beside loader.go so the serve path
// stays under the file/function size budgets; behaviour is unchanged.

import (
	"context"
	"log"
	"path/filepath"

	"github.com/shark2202/go_hive/go-hive/internal/channels"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

// newServeChannelHost builds the process-wide stdio channel host from the
// configured channels.<name> sections. A home that could not be resolved has
// no configuration to read, so it yields nil and GET /api/channels keeps its
// empty shape.
func newServeChannelHost(
	home hivehome.Home,
	manager *hiveserver.SessionManager,
	createSession func(hiveserver.CreateSessionParams) (*hiveserver.Session, error),
) *channels.Host {
	if home.Root == "" {
		return nil
	}
	return channels.NewHost(channels.HostOptions{
		Config:    hiveserver.HiveConfig(home),
		StateRoot: filepath.Join(home.Root, "channels"),
		BackendFor: func(name string) channels.SessionBackend {
			return hiveserver.NewChannelBackend(hiveserver.ChannelBackendOptions{
				Channel:       name,
				Home:          home,
				Manager:       manager,
				CreateSession: createSession,
			})
		},
		Logf: func(format string, args ...any) { log.Printf(format, args...) },
	})
}

// channelHostStates exposes the host's live states to AppDeps. nil means the
// route falls back to its empty response, exactly as before.
func channelHostStates(host *channels.Host) func() map[string]map[string]any {
	if host == nil {
		return nil
	}
	return host.States
}

// startServeChannelHost starts plugins immediately (defer evaluates the call
// at the defer statement) and returns the stop function for shutdown.
func startServeChannelHost(host *channels.Host) func() {
	host.Start(context.Background())
	return host.Stop
}
