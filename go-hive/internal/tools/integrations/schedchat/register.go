package schedchat

// Registry construction and assembly hooks. The root package calls NewRegistry
// (or Register into an existing registry); tests call NewRegistry with options
// to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

const (
	maxBodyBytes        = 8 << 20
	maxErrorBytes       = 500
	maxRetryWaitSeconds = 60
	// MaxRetries is the discord/mattermost 429 retry cap (upstream retries
	// twice before surfacing the classified rate-limit error).
	MaxRetries = 2
	// DiscordMaxMessage is the upstream Discord content limit in runes.
	DiscordMaxMessage = 2000
	// MattermostMaxText is the upstream Mattermost post text limit in runes.
	MattermostMaxText = 16383
)

// RetrySleep backs the discord/mattermost 429 retry loop; tests swap it for a
// no-op so retry coverage never waits.
var RetrySleep = time.Sleep

// NewRegistry builds a registry containing only the scheduling/chat tools
// (brevo, calcom, calendly, discord, mattermost), sharing the Runtime surface
// used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	rt := &Runtime{
		HTTP:    integrationkit.NewHTTPClient(nil),
		Now:     time.Now,
		Modules: modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(rt)
	}
	return Register(integrationkit.NewRegistry(rt))
}

// Register adds the five catalogs and their action implementations to reg. It
// is the assembly hook the root uses when it owns the kernel registry.
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{brevoTool, calcomTool, calendlyTool, discordTool, mattermostTool} {
		reg.AddTool(tool)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"brevo":      brevoImpls(),
		"calcom":     calcomImpls(),
		"calendly":   calendlyImpls(),
		"discord":    discordImpls(),
		"mattermost": mattermostImpls(),
	} {
		for action, impl := range actions {
			reg.AddImpl(tool, action, impl)
		}
	}
	return reg
}

// modules declares the public endpoints and credential variables.
// Mattermost has no default base: it always comes from MATTERMOST_URL.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"brevo_tool": {
			ID:      "brevo_tool",
			BaseURL: "https://api.brevo.com/v3",
			EnvVars: []string{"BREVO_API_KEY"},
		},
		"calcom_tool": {
			ID:      "calcom_tool",
			BaseURL: "https://api.cal.com/v1",
			EnvVars: []string{"CALCOM_API_KEY"},
		},
		"calendly_tool": {
			ID:      "calendly_tool",
			BaseURL: "https://api.calendly.com",
			EnvVars: []string{"CALENDLY_PAT"},
		},
		"discord_tool": {
			ID:      "discord_tool",
			BaseURL: "https://discord.com/api/v10",
			EnvVars: []string{"DISCORD_BOT_TOKEN"},
		},
		"mattermost_tool": {
			ID:      "mattermost_tool",
			EnvVars: []string{"MATTERMOST_ACCESS_TOKEN"},
		},
	}
}
