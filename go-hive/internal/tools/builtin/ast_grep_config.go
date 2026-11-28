package builtin

// The ast_grep registration gate is deliberately a tiny, standalone config
// reader: ast_grep is a Go-only post-1.0 tool and must stay off the model
// surface unless the operator opts in. It does not go through the integration
// registry or lsp.json.

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ASTGrepEnabled reports whether the opt-in ast_grep tool is enabled for home.
// Only one config level is read:
//
//   - <home>/ast_grep.json (user; an empty home falls back to HIVE_HOME)
//
// There is no project/session level. The session directory is the file tools'
// sandbox root, so the agent can write <sessionDir>/.hive/ast_grep.json; reading
// it would let the model opt itself into a tool the operator left off. See
// go_hive/docs/config-roots.html.
//
// The file is a JSON object with a top-level boolean "enabled". A missing file
// means disabled; malformed JSON and unreadable files fail closed (false).
// There is no environment-variable override.
func ASTGrepEnabled(home string) bool {
	if strings.TrimSpace(home) == "" {
		home = os.Getenv("HIVE_HOME")
	}
	if strings.TrimSpace(home) == "" {
		return false
	}
	enabled, found, err := readASTGrepConfig(filepath.Join(home, "ast_grep.json"))
	return found && err == nil && enabled
}

// readASTGrepConfig reads the operator-level config. found is false only when
// the file is absent; a present but unreadable or malformed file is returned
// with found=true and a non-nil error so the caller fails closed.
func readASTGrepConfig(path string) (enabled, found bool, err error) {
	// #nosec G304 -- path is built from HIVE_HOME plus a fixed filename; this
	// config reader intentionally follows the operator's path.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, false, nil
		}
		return false, true, err
	}
	var cfg struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false, true, err
	}
	// A present file without an explicit boolean stays disabled.
	return cfg.Enabled != nil && *cfg.Enabled, true, nil
}
