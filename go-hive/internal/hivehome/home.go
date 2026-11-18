package hivehome

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const EnvHome = "HIVE_HOME"

type Home struct {
	Root string
}

func Resolve() (Home, error) {
	if v := os.Getenv(EnvHome); v != "" {
		abs, err := filepath.Abs(v)
		if err != nil {
			return Home{}, fmt.Errorf("resolve %s: %w", EnvHome, err)
		}
		return Home{Root: abs}, nil
	}
	def, err := DefaultRoot()
	if err != nil {
		return Home{}, err
	}
	return Home{Root: def}, nil
}

func ResolveAt(root string) (Home, error) {
	if root == "" {
		return Home{}, errors.New("empty hive home")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return Home{}, fmt.Errorf("resolve home %q: %w", root, err)
	}
	return Home{Root: abs}, nil
}

func DefaultRoot() (string, error) {
	usr, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home: %w", err)
	}
	return filepath.Join(usr, ".hive"), nil
}

func Platform() string {
	return runtime.GOOS + "/" + runtime.GOARCH
}

func (h Home) EnsureDirs() error {
	dirs := []string{
		h.Root,
		h.QueensDir(),
		h.ColoniesDir(),
		h.MemoriesDir(),
		h.CredentialsDir(),
		h.MCPRegistryDir(),
		h.SecretsDir(),
		h.CacheDir(),
		h.EventLogsDir(),
		h.ToolArtifactsDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

func (h Home) ConfigurationFile() string { return filepath.Join(h.Root, "configuration.json") }
func (h Home) QueensDir() string         { return filepath.Join(h.Root, "queens") }
func (h Home) ColoniesDir() string       { return filepath.Join(h.Root, "colonies") }
func (h Home) MemoriesDir() string       { return filepath.Join(h.Root, "memories") }
func (h Home) CredentialsDir() string    { return filepath.Join(h.Root, "credentials") }
func (h Home) MCPRegistryDir() string    { return filepath.Join(h.Root, "mcp_registry") }
func (h Home) SecretsDir() string        { return filepath.Join(h.Root, "secrets") }
func (h Home) CacheDir() string          { return filepath.Join(h.Root, "cache") }
func (h Home) EventLogsDir() string      { return filepath.Join(h.Root, "event_logs") }
func (h Home) ToolArtifactsDir() string  { return filepath.Join(h.Root, "tool-artifacts") }
func (h Home) CompactionLogDir() string  { return filepath.Join(h.Root, "compaction_log") }
func (h Home) LLMLogsDir() string        { return filepath.Join(h.Root, "llm_logs") }
func (h Home) TrustedReposFile() string  { return filepath.Join(h.Root, "trusted_repos.json") }
func (h Home) QueenDir(id string) string { return filepath.Join(h.QueensDir(), id) }

// Parity: framework/config.py::queen_sessions_dir
func (h Home) QueenSessionsDir(queenName string) string {
	return filepath.Join(h.QueenDir(queenName), "sessions")
}

// Parity: framework/config.py::queen_session_dir
func (h Home) QueenSessionDir(queenName, sessionID string) string {
	return filepath.Join(h.QueenSessionsDir(queenName), sessionID)
}

// Parity: framework/config.py::colony_queens_dir
func (h Home) ColonyQueensDir(name string) string {
	return filepath.Join(h.ColonyDir(name), "queens")
}

// Parity: framework/config.py::colony_queen_session_dir
func (h Home) ColonyQueenSessionDir(colonyID, queenName, sessionID string) string {
	return filepath.Join(h.ColonyQueensDir(colonyID), queenName, "sessions", sessionID)
}

type Colony struct {
	Name      string
	Dir       string
	TrackerDB string
}

func (h Home) Colony(name string) Colony {
	dir := filepath.Join(h.ColoniesDir(), name)
	return Colony{Name: name, Dir: dir, TrackerDB: filepath.Join(dir, "tracker", "tracker.db")}
}

func (c Colony) ToDict() map[string]string {
	return map[string]string{
		"name":       c.Name,
		"dir":        c.Dir,
		"tracker_db": c.TrackerDB,
	}
}

func ColonyFromDict(data map[string]any) (Colony, bool) {
	name, nameOK := data["name"].(string)
	dir, dirOK := data["dir"].(string)
	tracker, trackerOK := data["tracker_db"].(string)
	if !nameOK || name == "" || !dirOK || !trackerOK {
		return Colony{}, false
	}
	return Colony{Name: name, Dir: dir, TrackerDB: tracker}, true
}

func (h Home) ColonyDir(name string) string { return h.Colony(name).Dir }
