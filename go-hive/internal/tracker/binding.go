package tracker

import (
	"os"
	"path/filepath"
	"strings"
)

// Parity: framework/host/colony_binding.py::ColonyBinding.from_dict
func BindingFromInputData(inputData map[string]any) *Binding {
	if inputData == nil {
		return nil
	}
	raw, ok := inputData["binding"]
	if !ok {
		return nil
	}
	switch value := raw.(type) {
	case nil:
		return nil
	case *Binding:
		if value == nil || value.Name == "" {
			return nil
		}
		copied := *value
		return &copied
	case Binding:
		if value.Name == "" {
			return nil
		}
		return &value
	case map[string]string:
		name := value["name"]
		dir, hasDir := value["dir"]
		trackerDB, hasTrackerDB := value["tracker_db"]
		if name == "" || !hasDir || !hasTrackerDB {
			return nil
		}
		return &Binding{Name: name, Dir: dir, TrackerDB: trackerDB}
	case map[string]any:
		name, ok := value["name"].(string)
		if !ok || name == "" {
			return nil
		}
		dir, ok := value["dir"].(string)
		if !ok {
			return nil
		}
		trackerDB, ok := value["tracker_db"].(string)
		if !ok {
			return nil
		}
		return &Binding{Name: name, Dir: dir, TrackerDB: trackerDB}
	default:
		return nil
	}
}

// BindingForColonyDir builds the binding for a known colony directory and
// provisions tracker.db first. It is the Go host's counterpart to the upstream
// sibling-colony fork flow (routes_sessions calls ensure_tracker_db before
// writing worker.json) and to run_worker's defensive ensure. A missing or
// non-directory path yields (nil, nil): a plain DM session must never
// fabricate a tracker.
//
// Parity: framework/server/routes_sessions.py::_create_sibling_colony_from_colony
func BindingForColonyDir(colonyDir, colonyID string) (*Binding, error) {
	if strings.TrimSpace(colonyDir) == "" {
		return nil, nil
	}
	info, err := os.Stat(colonyDir)
	if err != nil || !info.IsDir() {
		return nil, nil
	}
	dir, err := filepath.Abs(colonyDir)
	if err != nil {
		dir = colonyDir
	}
	trackerDB, err := EnsureTrackerDB(dir)
	if err != nil {
		return nil, err
	}
	name := colonyID
	if name == "" {
		name = filepath.Base(dir)
	}
	return &Binding{Name: name, Dir: dir, TrackerDB: trackerDB}, nil
}
