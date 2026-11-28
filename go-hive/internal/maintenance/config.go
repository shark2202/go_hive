package maintenance

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/pyenv"
)

type RetentionConfig struct {
	Enabled             bool   `json:"enabled"`
	Mode                string `json:"mode"`
	ArchiveDir          string `json:"archive_dir"`
	EventLogsDays       int    `json:"event_logs_days"`
	LLMLogsDays         int    `json:"llm_logs_days"`
	CompactionLogDays   int    `json:"compaction_log_days"`
	ToolArtifactsDays   int    `json:"tool_artifacts_days"`
	RotatedLogsDays     int    `json:"rotated_logs_days"`
	WorkerDeepCleanDays int    `json:"worker_deep_clean_days"`
	QueenHygieneDays    int    `json:"queen_hygiene_days"`
	EventsRewriteMin    int64  `json:"events_rewrite_min_bytes"`
	ActiveGraceHours    int    `json:"active_grace_hours"`
	IOSleepMS           int    `json:"io_sleep_ms"`
	BatchSize           int    `json:"batch_size"`
	JunkMinBytes        int64  `json:"junk_min_bytes"`
}

func DefaultRetentionConfig() RetentionConfig {
	return RetentionConfig{
		Enabled:             true,
		Mode:                "archive",
		ArchiveDir:          "",
		EventLogsDays:       7,
		LLMLogsDays:         7,
		CompactionLogDays:   7,
		ToolArtifactsDays:   14,
		RotatedLogsDays:     30,
		WorkerDeepCleanDays: 14,
		QueenHygieneDays:    30,
		EventsRewriteMin:    1_000_000,
		ActiveGraceHours:    48,
		IOSleepMS:           10,
		BatchSize:           200,
		JunkMinBytes:        50_000_000,
	}
}

type retentionField struct {
	name string
	set  func(json.RawMessage) bool
	env  func(string)
}

func (c *RetentionConfig) fields() []retentionField {
	return []retentionField{
		{"enabled", func(raw json.RawMessage) bool {
			var v bool
			if json.Unmarshal(raw, &v) != nil {
				return false
			}
			c.Enabled = v
			return true
		}, func(v string) {
			c.Enabled = !(strings.ToLower(strings.TrimSpace(v)) == "0" ||
				strings.ToLower(strings.TrimSpace(v)) == "false" ||
				strings.ToLower(strings.TrimSpace(v)) == "no" ||
				strings.TrimSpace(v) == "")
		}},
		{"mode", func(raw json.RawMessage) bool {
			var v string
			if json.Unmarshal(raw, &v) != nil {
				return false
			}
			c.Mode = v
			return true
		}, func(v string) { c.Mode = v }},
		{"archive_dir", func(raw json.RawMessage) bool {
			var v string
			if json.Unmarshal(raw, &v) != nil {
				return false
			}
			c.ArchiveDir = v
			return true
		}, func(v string) { c.ArchiveDir = v }},
		{"event_logs_days", intField(&c.EventLogsDays), func(v string) { intEnv(&c.EventLogsDays, v) }},
		{"llm_logs_days", intField(&c.LLMLogsDays), func(v string) { intEnv(&c.LLMLogsDays, v) }},
		{"compaction_log_days", intField(&c.CompactionLogDays), func(v string) { intEnv(&c.CompactionLogDays, v) }},
		{"tool_artifacts_days", intField(&c.ToolArtifactsDays), func(v string) { intEnv(&c.ToolArtifactsDays, v) }},
		{"rotated_logs_days", intField(&c.RotatedLogsDays), func(v string) { intEnv(&c.RotatedLogsDays, v) }},
		{"worker_deep_clean_days", intField(&c.WorkerDeepCleanDays), func(v string) { intEnv(&c.WorkerDeepCleanDays, v) }},
		{"queen_hygiene_days", intField(&c.QueenHygieneDays), func(v string) { intEnv(&c.QueenHygieneDays, v) }},
		{"events_rewrite_min_bytes", int64Field(&c.EventsRewriteMin), func(v string) { int64Env(&c.EventsRewriteMin, v) }},
		{"active_grace_hours", intField(&c.ActiveGraceHours), func(v string) { intEnv(&c.ActiveGraceHours, v) }},
		{"io_sleep_ms", intField(&c.IOSleepMS), func(v string) { intEnv(&c.IOSleepMS, v) }},
		{"batch_size", intField(&c.BatchSize), func(v string) { intEnv(&c.BatchSize, v) }},
		{"junk_min_bytes", int64Field(&c.JunkMinBytes), func(v string) { int64Env(&c.JunkMinBytes, v) }},
	}
}

func intField(dst *int) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		n, ok := intFromRaw(raw)
		if !ok {
			return false
		}
		*dst = int(n)
		return true
	}
}

func int64Field(dst *int64) func(json.RawMessage) bool {
	return func(raw json.RawMessage) bool {
		n, ok := intFromRaw(raw)
		if !ok {
			return false
		}
		*dst = n
		return true
	}
}

func intFromRaw(raw json.RawMessage) (int64, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return 0, false
	}
	num, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	if strings.ContainsAny(num.String(), ".eE") {
		return 0, false
	}
	n, err := num.Int64()
	if err != nil {
		return 0, false
	}
	return n, true
}

func intEnv(dst *int, v string) {
	if n, ok := pyenv.ParseInt(v); ok {
		*dst = n
	}
}

func int64Env(dst *int64, v string) {
	if n, ok := pyenv.ParseInt(v); ok {
		*dst = int64(n)
	}
}

func LoadRetentionConfig(home hivehome.Home) RetentionConfig {
	cfg := DefaultRetentionConfig()
	raw := readHiveConfig(home)

	if obj, ok := raw["retention"].(map[string]any); ok {
		for _, f := range cfg.fields() {
			value, present := obj[f.name]
			if !present {
				continue
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				continue
			}
			f.set(encoded)
		}
	}
	for _, f := range cfg.fields() {
		if v, ok := os.LookupEnv("HIVE_RETENTION_" + strings.ToUpper(f.name)); ok {
			f.env(v)
		}
	}
	return cfg
}

func readHiveConfig(home hivehome.Home) map[string]any {
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return map[string]any{}
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
