package eventbus

// Parity: framework/host/event_log.py

import (
	"log"
	"os"
	"path/filepath"
	"sync"
)

type EventLogFile struct {
	path string

	mu     sync.Mutex
	fh     *os.File
	broken bool
}

func NewEventLogFile(path string) (*EventLogFile, error) {
	l := &EventLogFile{path: path}
	if err := l.openLocked(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *EventLogFile) Path() string { return l.path }

func (l *EventLogFile) Broken() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.broken
}

func (l *EventLogFile) openLocked() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	fh, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	l.fh = fh
	return nil
}

func (l *EventLogFile) reopenLocked() error {
	if l.fh != nil {
		_ = l.fh.Close()
		l.fh = nil
	}
	return l.openLocked()
}

func (l *EventLogFile) writeLineLocked(line string) error {
	_, err := l.fh.WriteString(line + "\n")
	if err != nil {
		return err
	}
	return l.fh.Sync()
}

func (l *EventLogFile) Write(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.fh == nil {
		return
	}
	if l.writeLineLocked(line) == nil {
		return
	}

	if err := l.reopenLocked(); err != nil {
		l.fh = nil
		if !l.broken {
			l.broken = true
			log.Printf("Event log reopen failed for %s: %v", l.path, err)
		}
		return
	}
	if err := l.writeLineLocked(line); err != nil {
		_ = l.fh.Close()
		l.fh = nil
		if !l.broken {
			l.broken = true
			log.Printf("Event log reopen failed for %s: %v", l.path, err)
		}
		return
	}
	if l.broken {
		log.Printf("Event log recovered → %s", l.path)
	}
	l.broken = false
}

func (l *EventLogFile) Flush() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fh == nil {
		return
	}
	_ = l.fh.Sync()
}

func (l *EventLogFile) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fh == nil {
		return
	}
	_ = l.fh.Close()
	l.fh = nil
}
