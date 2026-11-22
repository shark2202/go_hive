package ai

// Registry construction and assembly hooks. The root package calls NewRegistry
// (or Register into an existing registry); tests call NewRegistry with options
// to point the modules at a fake server.

import (
	"os"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

const (
	maxBodyBytes  = 8 << 20
	maxErrorBytes = 500

	pineconeVersion = "2025-04"
	pineconeBaseURL = "https://api.pinecone.io"

	wandbEndpoint = "https://api.wandb.ai/graphql"

	langfuseHost = "https://cloud.langfuse.com"

	huggingFaceBase          = "https://huggingface.co/api"
	huggingFaceInferenceBase = "https://api-inference.huggingface.co/models"
	huggingFaceEndpointsBase = "https://api.endpoints.huggingface.cloud"

	visionAnnotateURL = "https://vision.googleapis.com/v1/images:annotate"
	VisionMaxBytes    = 10 * 1024 * 1024
)

// NewRegistry builds a registry containing only the AI-platform tools
// (pinecone, wandb, langfuse, huggingface, vision), sharing the Runtime
// surface used by the rest of the port.
func NewRegistry(opts ...Option) *Registry {
	rt := &Runtime{
		HTTP:    newHTTPClient(nil),
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
	for _, tool := range []*Tool{
		pineconeTool,
		wandbTool,
		langfuseTool,
		huggingFaceTool,
		visionTool,
	} {
		reg.AddTool(tool)
	}
	for tool, actions := range map[string]map[string]ActionFunc{
		"pinecone":    pineconeImpls(),
		"wandb":       wandbImpls(),
		"langfuse":    langfuseImpls(),
		"huggingface": huggingFaceImpls(),
		"vision":      visionImpls(),
	} {
		for action, impl := range actions {
			reg.AddImpl(tool, action, impl)
		}
	}
	return reg
}

// modules declares the public endpoints and credential variables.
// Langfuse derives its host from LANGFUSE_HOST (or WithBaseURL) because cloud
// tenants live in different regions; the Hugging Face inference and Endpoints
// APIs are separate hosts from the Hub.
func modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"pinecone_tool": {
			ID: "pinecone_tool", BaseURL: pineconeBaseURL,
			EnvVars: []string{"PINECONE_API_KEY"},
		},
		"wandb_tool": {
			ID: "wandb_tool", BaseURL: wandbEndpoint,
			EnvVars: []string{"WANDB_API_KEY"},
		},
		"langfuse_tool": {
			ID: "langfuse_tool",
			EnvVars: []string{
				"LANGFUSE_PUBLIC_KEY",
				"LANGFUSE_SECRET_KEY",
				"LANGFUSE_HOST",
			},
		},
		"huggingface_tool": {
			ID: "huggingface_tool", BaseURL: huggingFaceBase,
			EnvVars: []string{"HUGGINGFACE_TOKEN"},
		},
		"huggingface_inference_tool": {
			ID: "huggingface_inference_tool", BaseURL: huggingFaceInferenceBase,
		},
		"huggingface_endpoints_tool": {
			ID: "huggingface_endpoints_tool", BaseURL: huggingFaceEndpointsBase,
		},
		"vision_tool": {
			ID: "vision_tool", BaseURL: visionAnnotateURL,
			EnvVars: []string{"GOOGLE_CLOUD_VISION_API_KEY"},
		},
	}
}
