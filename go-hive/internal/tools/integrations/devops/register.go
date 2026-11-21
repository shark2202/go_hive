package devops

// Registry construction and assembly hooks. The root package calls NewRegistry
// (or Register into an existing registry); tests call NewRegistry with options
// to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/aden-hive/hive-go/internal/tools/integrationkit"
)

const (
	maxBodyBytes      = 8 << 20
	maxErrorBytes     = 500
	gitlabDefaultBase = "https://gitlab.com"
	s3DefaultHost     = "https://s3.amazonaws.com"
)

// NewRegistry builds a registry containing only the DevOps hosting tools
// (gitlab, aws_s3, vercel, docker_hub), sharing the Runtime surface used by
// the rest of the port.
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

// Register adds the four catalogs and their action implementations to reg. It
// is the assembly hook the root uses when it owns the kernel registry.
func Register(reg *Registry) *Registry {
	for _, tool := range []*Tool{gitlabTool, s3Tool, vercelTool, dockerHubTool} {
		reg.AddTool(tool)
	}
	for action, impl := range gitlabImpls() {
		reg.AddImpl("gitlab", action, impl)
	}
	for action, impl := range s3Impls() {
		reg.AddImpl("aws_s3", action, impl)
	}
	for action, impl := range vercelImpls() {
		reg.AddImpl("vercel", action, impl)
	}
	for action, impl := range dockerHubImpls() {
		reg.AddImpl("docker_hub", action, impl)
	}
	return reg
}

// modules declares the public endpoints and credential variables.
// AWS S3 keeps an empty BaseURL: its host is derived per bucket from the
// region unless WithBaseURL points it at a fake server.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"gitlab_tool": {
			ID: "gitlab_tool", BaseURL: gitlabDefaultBase,
			EnvVars: []string{"GITLAB_TOKEN"},
		},
		"aws_s3_tool": {
			ID:      "aws_s3_tool",
			EnvVars: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"},
		},
		"vercel_tool": {
			ID: "vercel_tool", BaseURL: "https://api.vercel.com",
			EnvVars: []string{"VERCEL_TOKEN"},
		},
		"docker_hub_tool": {
			ID: "docker_hub_tool", BaseURL: "https://hub.docker.com/v2",
			EnvVars: []string{"DOCKER_HUB_TOKEN"},
		},
	}
}
