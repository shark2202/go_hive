// Package logging holds the process log sink shared by the serve path.
//
// Parity: framework/observability/logging.py (the file-handler branch).
//
// Upstream attaches a rotating file handler at $HIVE_HOME/logs/hive-server.log
// (20 MiB x 3 backups) to the root logger so server stalls and 500s survive the
// console. Operators opt out in containers with HIVE_NO_FILE_LOG=1 — the gate
// is "the variable is set to a non-empty value", not a truthiness parse, so
// HIVE_NO_FILE_LOG=0 also disables the file, exactly like upstream's
// `if not os.getenv("HIVE_NO_FILE_LOG")`.
//
// Go's serve process logs through the standard library's `log` package (the
// same stream the /api/runtime-logs ring taps), so the sink is attached there.
//
// Line shape: upstream's HumanReadableFormatter writes
// "<%Y-%m-%d %H:%M:%S> [<LEVEL>] <message>". Go's process log carries no
// severity, so the aligned part is the timestamp: NewTimestampWriter stamps
// every record with the same "2006-01-02 15:04:05" prefix and callers clear
// the std log flags (log.SetFlags(0)) so the default "2006/01/02" prefix does
// not appear. The "[LEVEL]" tag and the LOG_FORMAT=json mode stay
// Python-only; see docs/observability-surface-diff.md.
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

const (
	// serverLogName matches upstream's file name under $HIVE_HOME/logs.
	serverLogName = "hive-server.log"
	// serverLogMaxBytes / serverLogBackups mirror RotatingFileHandler
	// (maxBytes=20 * 1024 * 1024, backupCount=3).
	serverLogMaxBytes = 20 * 1024 * 1024
	serverLogBackups  = 3
)

// envNonEmpty is the package-local spelling of the upstream string gate
// (`if not os.getenv(name)`), kept scanner-visible like every other reader.
func envNonEmpty(name string) bool { return pyenv.NonEmpty(name) }

// fileLogDisabled mirrors upstream's `if not os.getenv("HIVE_NO_FILE_LOG")`
// gate: any non-empty value (including "0") switches the file log off.
func fileLogDisabled() bool { return envNonEmpty("HIVE_NO_FILE_LOG") }

// timestampLayout is upstream's console/file timestamp shape
// (HumanReadableFormatter: "%Y-%m-%d %H:%M:%S"; RecentLogs uses the same).
const timestampLayout = "2006-01-02 15:04:05"

// NewTimestampWriter prefixes every record written through it with the
// upstream timestamp. The std log package invokes Write once per record after
// formatting, so one prefix per call matches Python's one-prefix-per-record
// formatter. Callers must also clear the std log flags (log.SetFlags(0)) or
// the default "2006/01/02 15:04:05" prefix would precede this one.
func NewTimestampWriter(w io.Writer) io.Writer {
	return &timestampWriter{w: w}
}

type timestampWriter struct{ w io.Writer }

func (t *timestampWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	stamp := time.Now().Format(timestampLayout) + " "
	buf := make([]byte, 0, len(stamp)+len(p))
	buf = append(buf, stamp...)
	buf = append(buf, p...)
	written, err := t.w.Write(buf)
	// Report bytes consumed from p, not from the stamped record.
	if written > len(stamp) {
		return written - len(stamp), err
	}
	return 0, err
}

// RotatingFile is an append-only writer that rotates a log file the way
// RotatingFileHandler does: when a write would exceed maxBytes, the base file
// becomes .1 and older backups shift up, dropping the oldest.
type RotatingFile struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	backups  int
	file     *os.File
	size     int64
}

// OpenServerLog opens $HIVE_HOME/logs/hive-server.log for appending.
// enabled is false when HIVE_NO_FILE_LOG is set; the caller then keeps its
// existing sinks untouched.
func OpenServerLog(homeDir string) (writer *RotatingFile, enabled bool, err error) {
	return openServerLogAt(filepath.Join(homeDir, "logs"), serverLogMaxBytes, serverLogBackups)
}

// OpenRotating opens (creating if needed) a rotating file for a dedicated log
// namespace, the way upstream's RotatingFileHandler(...) calls do for
// sentinel.log and hive-server.log.
func OpenRotating(path string, maxBytes int64, backups int) (*RotatingFile, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if backups < 1 {
		backups = 1
	}
	return &RotatingFile{path: path, maxBytes: maxBytes, backups: backups, file: file, size: info.Size()}, nil
}

func openServerLogAt(logDir string, maxBytes int64, backups int) (*RotatingFile, bool, error) {
	if fileLogDisabled() {
		return nil, false, nil
	}
	writer, err := OpenRotating(filepath.Join(logDir, serverLogName), maxBytes, backups)
	if err != nil {
		return nil, true, err
	}
	return writer, true, nil
}

// Write implements io.WriteCloser: append one record, rotating first when it
// would overflow the cap.
func (r *RotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return 0, os.ErrClosed
	}
	if r.size+int64(len(p)) > r.maxBytes {
		if err := r.rotateLocked(); err != nil {
			return 0, err
		}
	}
	n, err := r.file.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *RotatingFile) rotateLocked() error {
	if err := r.file.Close(); err != nil {
		return err
	}
	// .(backups-1) is dropped, every other backup shifts up one, base -> .1.
	_ = os.Remove(fmt.Sprintf("%s.%d", r.path, r.backups))
	for i := r.backups - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", r.path, i), fmt.Sprintf("%s.%d", r.path, i+1))
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	r.file = file
	r.size = 0
	return nil
}

// Close releases the file handle.
func (r *RotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

// Path is the base log file path (diagnostics and tests).
func (r *RotatingFile) Path() string { return r.path }
