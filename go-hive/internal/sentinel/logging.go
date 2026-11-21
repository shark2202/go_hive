package sentinel

// Parity: framework/sentinel/manager.py::_install_sentinel_log_file
//
// Opt-in: with HIVE_SENTINEL_LOG set to a non-empty value, the whole sentinel
// namespace is diverted into a dedicated rotating file at
// $HIVE_HOME/logs/sentinel.log so a long debug session can watch sentinel in
// isolation (tail -f) instead of hunting it in the mixed main stream. While the
// file is installed, sentinel lines deliberately stay OFF the main console —
// upstream sets propagate = False — and the install is idempotent.
//
// Python's gate is `if not os.environ.get("HIVE_SENTINEL_LOG"): return`, so any
// non-empty value (including "0") enables the file. Nothing here touches the
// logger when the variable is unset: default behaviour is unchanged.

import (
	"io"
	"log"
	"path/filepath"
	"sync"

	hivelogging "github.com/aden-hive/hive-go/internal/logging"
	"github.com/aden-hive/hive-go/internal/pyenv"
)

const (
	sentinelLogName     = "sentinel.log"
	sentinelLogMaxBytes = 5_000_000
	sentinelLogBackups  = 3
)

// envNonEmpty is the package-local spelling of the upstream string gate
// (`if not os.environ.get(name)`), kept scanner-visible.
func envNonEmpty(name string) bool { return pyenv.NonEmpty(name) }

var sentinelLogState struct {
	mu     sync.Mutex
	logger *log.Logger
	file   io.WriteCloser
}

// ConfigureLogging installs the dedicated sentinel file sink when the env gate
// is set. homeRoot is $HIVE_HOME. Idempotent; errors leave the default logger
// in place (a broken log path must never take sentinel down).
func ConfigureLogging(homeRoot string) {
	if !envNonEmpty("HIVE_SENTINEL_LOG") {
		return
	}
	sentinelLogState.mu.Lock()
	defer sentinelLogState.mu.Unlock()
	if sentinelLogState.logger != nil {
		return
	}
	path := filepath.Join(homeRoot, "logs", sentinelLogName)
	file, err := hivelogging.OpenRotating(path, sentinelLogMaxBytes, sentinelLogBackups)
	if err != nil {
		log.Printf("sentinel: file log unavailable (%v); keeping the main log", err)
		return
	}
	sentinelLogState.file = file
	// Upstream writes "%(asctime)s %(levelname)-7s %(name)s | %(message)s".
	// Go's sentinel sink has no per-line severity or module name, so the
	// aligned part is the timestamp: the same "2006-01-02 15:04:05" prefix
	// upstream's asctime produces.
	sentinelLogState.logger = log.New(hivelogging.NewTimestampWriter(file), "", 0)
	log.Printf("sentinel: diverting sentinel logs to %s", path)
}

// sentinelLogf is the package's log call. With the opt-in file installed the
// line goes there and nowhere else; otherwise it behaves exactly like
// log.Printf did before this seam existed.
func sentinelLogf(format string, args ...any) {
	sentinelLogState.mu.Lock()
	logger := sentinelLogState.logger
	sentinelLogState.mu.Unlock()
	if logger != nil {
		logger.Printf(format, args...)
		return
	}
	log.Printf(format, args...)
}

// resetSentinelLoggingForTest drops the installed sink so a test can flip the
// env gate between cases.
func resetSentinelLoggingForTest() {
	sentinelLogState.mu.Lock()
	file := sentinelLogState.file
	sentinelLogState.file = nil
	sentinelLogState.logger = nil
	sentinelLogState.mu.Unlock()
	if file != nil {
		_ = file.Close()
	}
}
