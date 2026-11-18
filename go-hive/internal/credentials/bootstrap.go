package credentials

import "os"

type bootstrapConfig struct {
	extraEnvVars []string
	llmEnvVar    string
	shell        *ShellType
}

type BootstrapOption func(*bootstrapConfig)

func WithExtraEnvVars(vars ...string) BootstrapOption {
	return func(c *bootstrapConfig) { c.extraEnvVars = append(c.extraEnvVars, vars...) }
}

func WithLLMEnvVar(name string) BootstrapOption {
	return func(c *bootstrapConfig) { c.llmEnvVar = name }
}

func WithBootstrapShell(shell ShellType) BootstrapOption {
	return func(c *bootstrapConfig) { c.shell = &shell }
}

func EnsureCredentialKeyEnv(opts ...BootstrapOption) {
	config := &bootstrapConfig{}
	for _, opt := range opts {
		opt(config)
	}

	_, _ = LoadCredentialKey()
	_, _ = LoadAdenAPIKey()

	shell := DetectShell()
	if config.shell != nil {
		shell = *config.shell
	}

	fillFromShell := func(name string) {
		if name == "" || name == CredentialKeyEnvVar || name == AdenEnvVar {
			return
		}
		if os.Getenv(name) != "" {
			return
		}
		if found, value := CheckEnvVarInShellConfig(name, shell); found && value != "" {
			_ = os.Setenv(name, value)
		}
	}

	for _, name := range config.extraEnvVars {
		fillFromShell(name)
	}
	fillFromShell(config.llmEnvVar)
}
