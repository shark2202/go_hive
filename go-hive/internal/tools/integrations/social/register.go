package social

// Registry construction and assembly hooks. The root package calls
// NewRegistry (or Register into an existing registry); tests call
// NewRegistry with options to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

const (
	maxBodyBytes  = 8 << 20
	maxErrorBytes = 500
)

const (
	youtubeModuleID  = "youtube_tool"
	redditModuleID   = "reddit_tool"
	twitterModuleID  = "twitter_tool"
	twilioModuleID   = "twilio_tool"
	pushoverModuleID = "pushover_tool"

	youtubeDefaultBase  = "https://www.googleapis.com/youtube/v3"
	twitterDefaultBase  = "https://api.x.com/2"
	twilioDefaultBase   = "https://api.twilio.com"
	pushoverDefaultBase = "https://api.pushover.net/1"
	redditDefaultBase   = "https://oauth.reddit.com"
)

// NewRegistry builds a registry containing only the social-media tools
// (youtube, reddit, twitter, twilio, pushover), sharing the Runtime surface
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

// Register adds the five catalogs and their action implementations to reg.
// It is the assembly hook the root uses when it owns the kernel registry.
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{youtubeTool, redditTool, twitterTool, twilioTool, pushoverTool} {
		reg.AddTool(tool)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"youtube":  youtubeImpls(),
		"reddit":   redditImpls(),
		"twitter":  twitterImpls(),
		"twilio":   twilioImpls(),
		"pushover": pushoverImpls(),
	} {
		for action, impl := range actions {
			reg.AddImpl(tool, action, impl)
		}
	}
	return reg
}

// modules declares the default endpoints and credential variables.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		youtubeModuleID: {
			ID: youtubeModuleID, BaseURL: youtubeDefaultBase,
			EnvVars: []string{"YOUTUBE_API_KEY"},
		},
		redditModuleID: {
			ID: redditModuleID, BaseURL: redditDefaultBase,
			EnvVars: []string{"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET"},
		},
		twitterModuleID: {
			ID: twitterModuleID, BaseURL: twitterDefaultBase,
			EnvVars: []string{"X_BEARER_TOKEN"},
		},
		twilioModuleID: {
			ID: twilioModuleID, BaseURL: twilioDefaultBase,
			EnvVars: []string{"TWILIO_ACCOUNT_SID", "TWILIO_AUTH_TOKEN"},
		},
		pushoverModuleID: {
			ID: pushoverModuleID, BaseURL: pushoverDefaultBase,
			EnvVars: []string{"PUSHOVER_API_TOKEN"},
		},
	}
}
