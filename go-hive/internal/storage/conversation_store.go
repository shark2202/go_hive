package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

type FileConversationStore struct {
	base        string
	partsDir    string
	partialsDir string
}

func NewFileConversationStore(basePath string) *FileConversationStore {
	base := filepath.Clean(basePath)
	return &FileConversationStore{
		base:        base,
		partsDir:    filepath.Join(base, "parts"),
		partialsDir: filepath.Join(base, "partials"),
	}
}

func (s *FileConversationStore) BasePath() string { return s.base }

func (s *FileConversationStore) PartsDir() string { return s.partsDir }

func (s *FileConversationStore) writeJSON(path string, data map[string]any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(path, payload, 0o644)
}

func (s *FileConversationStore) readJSON(path string) *map[string]any {
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	return &out
}

func partName(seq int) string { return fmt.Sprintf("%010d.json", seq) }

func jsonFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := []string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".json") {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	sort.Strings(out)
	return out, nil
}

func (s *FileConversationStore) WritePart(seq int, data map[string]any) error {
	return s.writeJSON(filepath.Join(s.partsDir, partName(seq)), data)
}

func (s *FileConversationStore) ReadParts() ([]map[string]any, error) {
	files, err := jsonFiles(s.partsDir)
	if err != nil {
		return nil, err
	}
	parts := []map[string]any{}
	for _, f := range files {
		if data := s.readJSON(f); data != nil {
			parts = append(parts, *data)
		}
	}
	return parts, nil
}

func (s *FileConversationStore) WriteMeta(data map[string]any) error {
	return s.writeJSON(filepath.Join(s.base, "meta.json"), data)
}

func (s *FileConversationStore) ReadMeta() *map[string]any {
	return s.readJSON(filepath.Join(s.base, "meta.json"))
}

func (s *FileConversationStore) WriteCursor(data map[string]any) error {
	return s.writeJSON(filepath.Join(s.base, "cursor.json"), data)
}

func (s *FileConversationStore) ReadCursor() *map[string]any {
	return s.readJSON(filepath.Join(s.base, "cursor.json"))
}

func (s *FileConversationStore) WritePartial(seq int, data map[string]any) error {
	return s.writeJSON(filepath.Join(s.partialsDir, partName(seq)), data)
}

func (s *FileConversationStore) ReadPartial(seq int) *map[string]any {
	return s.readJSON(filepath.Join(s.partialsDir, partName(seq)))
}

func (s *FileConversationStore) ReadAllPartials() ([]map[string]any, error) {
	files, err := jsonFiles(s.partialsDir)
	if err != nil {
		return nil, err
	}
	partials := []map[string]any{}
	for _, f := range files {
		if data := s.readJSON(f); data != nil {
			partials = append(partials, *data)
		}
	}
	return partials, nil
}

func (s *FileConversationStore) ClearPartial(seq int) error {
	err := os.Remove(filepath.Join(s.partialsDir, partName(seq)))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *FileConversationStore) DeletePartsBefore(seq int, runID string) error {
	files, err := jsonFiles(s.partsDir)
	if err != nil {
		return err
	}
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), ".json")
		fileSeq, err := strconv.Atoi(stem)
		if err != nil {
			return fmt.Errorf("invalid part filename: %s", stem)
		}
		if fileSeq < seq {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

func (s *FileConversationStore) Close() error { return nil }

func (s *FileConversationStore) Clear() error {
	for _, dir := range []string{s.partsDir, s.partialsDir} {
		files, err := jsonFiles(dir)
		if err != nil {
			return err
		}
		for _, f := range files {
			if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, name := range []string{"cursor.json", "meta.json"} {
		p := filepath.Join(s.base, name)
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *FileConversationStore) Destroy() error {
	if _, err := os.Stat(s.base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.RemoveAll(s.base)
}
