package triggers

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/aden-hive/hive-go/internal/atomicfs"
)

const TriggersFileName = "triggers.json"

type WorkerPathSession interface {
	WorkerPath() string
}

func ReadAgentTriggersJSON(agentPath string) []map[string]any {
	path := filepath.Join(agentPath, TriggersFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []map[string]any{}
		}
		return []map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return []map[string]any{}
	}
	list, ok := decoded.([]any)
	if !ok {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func WriteAgentTriggersJSON(agentPath string, triggers []map[string]any) error {
	if triggers == nil {
		triggers = []map[string]any{}
	}
	data, err := marshalLikePython(triggers)
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(filepath.Join(agentPath, TriggersFileName), data, 0o644)
}

func marshalLikePython(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("marshal triggers: %w", err)
	}
	return buf.Bytes(), nil
}

func SaveTriggerToAgent(session WorkerPathSession, triggerID string, tdef *TriggerDefinition) error {
	agentPath := session.WorkerPath()
	if agentPath == "" || tdef == nil {
		return nil
	}
	triggers := ReadAgentTriggersJSON(agentPath)
	kept := make([]map[string]any, 0, len(triggers)+1)
	for _, t := range triggers {
		if strVal(t["id"]) == triggerID {
			continue
		}
		kept = append(kept, t)
	}

	config := tdef.TriggerConfig
	if config == nil {
		config = map[string]any{}
	}
	name := tdef.Description
	if name == "" {
		name = tdef.ID
	}
	kept = append(kept, map[string]any{
		"id":             tdef.ID,
		"name":           name,
		"trigger_type":   tdef.TriggerType,
		"trigger_config": config,
		"task":           tdef.Task,
		"enabled":        tdef.Enabled,
		"last_fired_at":  nullableString(tdef.LastFiredAt),
		"next_due_at":    nullableString(tdef.NextDueAt),
	})
	return WriteAgentTriggersJSON(agentPath, kept)
}

func RemoveTriggerFromAgent(session WorkerPathSession, triggerID string) error {
	agentPath := session.WorkerPath()
	if agentPath == "" {
		return nil
	}
	triggers := ReadAgentTriggersJSON(agentPath)
	updated := make([]map[string]any, 0, len(triggers))
	for _, t := range triggers {
		if strVal(t["id"]) == triggerID {
			continue
		}
		updated = append(updated, t)
	}
	if len(updated) == len(triggers) {
		return nil
	}
	return WriteAgentTriggersJSON(agentPath, updated)
}

func nullableString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
