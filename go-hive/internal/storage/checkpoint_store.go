package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/schemas"
)

type CheckpointStore struct {
	BasePath       string
	CheckpointsDir string
	IndexPath      string

	mu sync.Mutex
}

func NewCheckpointStore(basePath string) *CheckpointStore {
	base := filepath.Clean(basePath)
	dir := filepath.Join(base, "checkpoints")
	return &CheckpointStore{
		BasePath:       base,
		CheckpointsDir: dir,
		IndexPath:      filepath.Join(dir, "index.json"),
	}
}

func (s *CheckpointStore) SaveCheckpoint(checkpoint *schemas.Checkpoint) error {
	if err := os.MkdirAll(s.CheckpointsDir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.CheckpointsDir, checkpoint.CheckpointID+".json")
	if err := atomicfs.WriteJSON(path, checkpoint, 0o644); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateIndexAdd(checkpoint)
}

func (s *CheckpointStore) LoadCheckpoint(checkpointID string) *schemas.Checkpoint {
	if checkpointID == "" {
		index, err := s.LoadIndex()
		if err != nil || index == nil || index.LatestCheckpointID == nil {
			return nil
		}
		checkpointID = *index.LatestCheckpointID
	}
	path := filepath.Join(s.CheckpointsDir, checkpointID+".json")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var cp schemas.Checkpoint
	if err := readJSONFile(path, &cp); err != nil {
		return nil
	}
	return &cp
}

func (s *CheckpointStore) LoadIndex() (*schemas.CheckpointIndex, error) {
	if _, err := os.Stat(s.IndexPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var index schemas.CheckpointIndex
	if err := readJSONFile(s.IndexPath, &index); err != nil {
		return nil, nil
	}
	return &index, nil
}

func (s *CheckpointStore) ListCheckpoints(checkpointType string, isClean *bool) ([]schemas.CheckpointSummary, error) {
	index, err := s.LoadIndex()
	if err != nil {
		return nil, err
	}
	if index == nil {
		return []schemas.CheckpointSummary{}, nil
	}
	out := []schemas.CheckpointSummary{}
	for _, cp := range index.Checkpoints {
		if checkpointType != "" && cp.CheckpointType != checkpointType {
			continue
		}
		if isClean != nil && cp.IsClean != *isClean {
			continue
		}
		out = append(out, cp)
	}
	return out, nil
}

func (s *CheckpointStore) DeleteCheckpoint(checkpointID string) bool {
	path := filepath.Join(s.CheckpointsDir, checkpointID+".json")
	if _, err := os.Stat(path); err != nil {
		return false
	}
	if err := os.Remove(path); err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.updateIndexRemove(checkpointID)
	return true
}

func (s *CheckpointStore) PruneCheckpoints(maxAgeDays int) int {
	index, err := s.LoadIndex()
	if err != nil || index == nil || len(index.Checkpoints) == 0 {
		return 0
	}
	cutoff := time.Now().Add(-time.Duration(maxAgeDays) * 24 * time.Hour)
	old := []string{}
	for _, cp := range index.Checkpoints {
		created, err := parsePythonDatetime(cp.CreatedAt)
		if err != nil {
			continue
		}
		if created.Before(cutoff) {
			old = append(old, cp.CheckpointID)
		}
	}
	deleted := 0
	for _, id := range old {
		if s.DeleteCheckpoint(id) {
			deleted++
		}
	}
	return deleted
}

func (s *CheckpointStore) CheckpointExists(checkpointID string) bool {
	_, err := os.Stat(filepath.Join(s.CheckpointsDir, checkpointID+".json"))
	return err == nil
}

func (s *CheckpointStore) updateIndexAdd(checkpoint *schemas.Checkpoint) error {
	index, _ := s.LoadIndex()
	if index == nil {
		index = &schemas.CheckpointIndex{SessionID: checkpoint.SessionID, Checkpoints: []schemas.CheckpointSummary{}}
	}
	index.AddCheckpoint(*checkpoint)
	if err := os.MkdirAll(s.CheckpointsDir, 0o755); err != nil {
		return err
	}
	return atomicfs.WriteJSON(s.IndexPath, index, 0o644)
}

func (s *CheckpointStore) updateIndexRemove(checkpointID string) {
	index, _ := s.LoadIndex()
	if index == nil {
		return
	}
	kept := []schemas.CheckpointSummary{}
	for _, cp := range index.Checkpoints {
		if cp.CheckpointID != checkpointID {
			kept = append(kept, cp)
		}
	}
	index.Checkpoints = kept
	index.TotalCheckpoints = len(kept)
	if index.LatestCheckpointID != nil && *index.LatestCheckpointID == checkpointID {
		if len(kept) > 0 {
			latest := kept[len(kept)-1].CheckpointID
			index.LatestCheckpointID = &latest
		} else {
			index.LatestCheckpointID = nil
		}
	}
	_ = atomicfs.WriteJSON(s.IndexPath, index, 0o644)
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func parsePythonDatetime(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999Z0700"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", s)
}
