package credentials

// Python-format logging for the credential subsystem.
//
// hive-cred's HIVE_CRED_LOG sink mirrors upstream's logging setup
// (framework/credentials/cli.py::_setup_cli_logging): every record is written
// as
//
//	%(asctime)s %(levelname)-7s [%(name)s] %(message)s   (datefmt="%H:%M:%S")
//
// e.g. `20:12:25 WARNING [framework.credentials.storage] Generated new …`.
// Go's standard logger has no level or logger-name concept, so the credential
// packages emit through these helpers, each carrying the level and the Python
// logger name it corresponds to. Outside hive-cred no sink is installed and the
// helpers fall back to log.Printf (the historical stderr behavior).

import (
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"
)

const (
	levelDebug   = "DEBUG"
	levelInfo    = "INFO"
	levelWarning = "WARNING"
)

var (
	credLogMu   sync.RWMutex
	credLogSink *log.Logger
)

// SetLogSink installs the HIVE_CRED_LOG file sink. A nil writer removes it and
// restores the standard-logger fallback.
func SetLogSink(writer io.Writer) {
	credLogMu.Lock()
	defer credLogMu.Unlock()
	if writer == nil {
		credLogSink = nil
		return
	}
	credLogSink = log.New(writer, "", 0)
}

// LogDebug emits one DEBUG record for the named Python logger.
func LogDebug(name, format string, args ...any) { logAt(levelDebug, name, format, args...) }

// LogInfo emits one INFO record for the named Python logger.
func LogInfo(name, format string, args ...any) { logAt(levelInfo, name, format, args...) }

// LogWarning emits one WARNING record for the named Python logger.
func LogWarning(name, format string, args ...any) { logAt(levelWarning, name, format, args...) }

func logAt(level, name, format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	credLogMu.RLock()
	sink := credLogSink
	credLogMu.RUnlock()
	if sink == nil {
		// Historical behavior for serve / runtime callers: the message already
		// carries its "credentials:" prefix.
		log.Printf("%s", message)
		return
	}
	// The Python logger name replaces the hand-written "credentials:" prefix.
	message = strings.TrimPrefix(message, "credentials: ")
	sink.Printf("%s %-7s [%s] %s", time.Now().Format("15:04:05"), level, name, message)
}
