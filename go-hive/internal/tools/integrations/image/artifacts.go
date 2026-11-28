package image

// Artifact-directory resolution for generated images, mirroring the Python
// order:
//
//	<HIVE_STORAGE_PATH>/data       per-agent session data dir
//	<HIVE_HOME>/tool-artifacts     shared install dir
//	~/.hive/tool-artifacts         OSS default
//
// A leading "~" is expanded in either variable: the framework injects absolute
// paths, but a hand-run process may export "~/…" and must not create a literal
// "~" directory. Python expands HIVE_HOME only; expanding HIVE_STORAGE_PATH
// too is the stricter of the two and is what this port already did for
// generated images.
//
// This file deliberately mirrors the root batch3_shared.go resolver that
// web_scrape still uses. The two converge when the resolver moves to
// integrationkit.

import (
	"os"
	"path/filepath"
	"strings"
)

// storageEnv carries the injectable filesystem environment of the artifact
// writer. The zero value reads the process environment.
type storageEnv struct {
	getenv      func(string) string
	userHomeDir func() (string, error)
}

func (env storageEnv) value(name string) string {
	if env.getenv == nil {
		return strings.TrimSpace(os.Getenv(name))
	}
	return strings.TrimSpace(env.getenv(name))
}

func (env storageEnv) home() (string, error) {
	if env.userHomeDir != nil {
		return env.userHomeDir()
	}
	return os.UserHomeDir()
}

// artifactDir is the single artifact-directory resolver for the image tool.
func artifactDir(env storageEnv) (string, error) {
	if storage := env.value("HIVE_STORAGE_PATH"); storage != "" {
		return filepath.Join(expandHome(storage, env), "data"), nil
	}
	if home := env.value("HIVE_HOME"); home != "" {
		return filepath.Join(expandHome(home, env), "tool-artifacts"), nil
	}
	userHome, err := env.home()
	if err != nil {
		return "", err
	}
	return filepath.Join(userHome, ".hive", "tool-artifacts"), nil
}

// expandHome expands a leading "~" without touching any other path.
func expandHome(path string, env storageEnv) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := env.home()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
