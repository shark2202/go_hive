package sentinel

// Stable identity for this runtime instance — the Hive Inbox routing key.
//
// A reply is routed back to the runtime that ORIGINATED the escalation, so
// each runtime needs a runtime id that is globally unique and stable across
// the life of the process:
//
//   - cloud: the sandbox id (HIVE_RUNTIME_ID / E2B_SANDBOX_ID), so it
//     reconciles with the backend account VM.
//   - local: a uuid persisted under $HIVE_HOME/runtime_id so it survives
//     restarts of a desktop-spawned subprocess.
//
// Both are overridable via HIVE_RUNTIME_ID / HIVE_RUNTIME_KIND.
//
// Parity: core/framework/sentinel/runtime_identity.py

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const runtimeIDFilename = "runtime_id"

// DefaultHome resolves the active Hive home (~/.hive unless HIVE_HOME is set).
func DefaultHome() string {
	home, err := hivehome.Resolve()
	if err != nil {
		return ""
	}
	return home.Root
}

// RuntimeIdentity resolves and caches this runtime's id and kind.
type RuntimeIdentity struct {
	// IDPath is where a locally-generated id is persisted. Empty means
	// <DefaultHome()>/runtime_id.
	IDPath string
	// Getenv is injectable for tests. Nil means os.Getenv.
	Getenv func(string) string

	mu     sync.Mutex
	cached string
}

// NewRuntimeIdentity returns an identity persisted under hiveRoot.
func NewRuntimeIdentity(hiveRoot string) *RuntimeIdentity {
	if hiveRoot == "" {
		hiveRoot = DefaultHome()
	}
	return &RuntimeIdentity{IDPath: filepath.Join(hiveRoot, runtimeIDFilename)}
}

var (
	defaultRuntimeOnce sync.Once
	defaultRuntime     *RuntimeIdentity
)

// DefaultRuntimeIdentity returns the process-wide runtime identity.
func DefaultRuntimeIdentity() *RuntimeIdentity {
	defaultRuntimeOnce.Do(func() { defaultRuntime = NewRuntimeIdentity("") })
	return defaultRuntime
}

func (r *RuntimeIdentity) getenv(name string) string {
	if r.Getenv != nil {
		return r.Getenv(name)
	}
	return os.Getenv(name)
}

// Kind returns "cloud" or "local". An explicit HIVE_RUNTIME_KIND wins;
// otherwise a runtime/sandbox id env implies cloud, else local.
func (r *RuntimeIdentity) Kind() string {
	kind := strings.ToLower(strings.TrimSpace(r.getenv("HIVE_RUNTIME_KIND")))
	if kind == "local" || kind == "cloud" {
		return kind
	}
	if strings.TrimSpace(r.getenv("HIVE_RUNTIME_ID")) != "" ||
		strings.TrimSpace(r.getenv("E2B_SANDBOX_ID")) != "" {
		return "cloud"
	}
	return "local"
}

// ID returns the stable runtime id: env override > sandbox id > persisted
// local uuid, created lazily.
func (r *RuntimeIdentity) ID() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cached != "" {
		return r.cached
	}

	if rid := strings.TrimSpace(r.getenv("HIVE_RUNTIME_ID")); rid != "" {
		r.cached = rid
		return rid
	}
	if rid := strings.TrimSpace(r.getenv("E2B_SANDBOX_ID")); rid != "" {
		r.cached = rid
		return rid
	}

	path := r.IDPath
	if path == "" {
		path = filepath.Join(DefaultHome(), runtimeIDFilename)
	}
	if data, err := os.ReadFile(path); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			r.cached = value
			return value
		}
	} else if !os.IsNotExist(err) {
		log.Printf("sentinel: could not read runtime_id, regenerating: %v", err)
	}

	rid := "local-" + NewUUID4Hex()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("sentinel: could not persist runtime_id; using ephemeral: %v", err)
	} else if err := os.WriteFile(path, []byte(rid), 0o644); err != nil {
		log.Printf("sentinel: could not persist runtime_id; using ephemeral: %v", err)
	}
	r.cached = rid
	return rid
}

// Reset drops the cached id (tests re-point the home directory).
func (r *RuntimeIdentity) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cached = ""
}
