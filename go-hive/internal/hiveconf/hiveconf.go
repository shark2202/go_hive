// Package hiveconf reads the global configuration.json (<HIVE_HOME>) with the
// shared, type-strict rules the Python reference uses.
//
// It exists so low-level packages (the colony worker loop, the CLI) can honour
// a global knob without importing the HTTP route package — the readers here are
// deliberately tiny and dependency-free; the model-slot readers that need the
// llm/tools catalogs stay in internal/server.
//
// Parity: framework/config.py::get_hive_config, get_max_tool_result_chars
package hiveconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
)

// Load reads configuration.json. A missing file, bad JSON or a non-object
// document reads as {} so every caller falls back to its default, exactly as
// upstream's get_hive_config does.
func Load(home hivehome.Home) map[string]any {
	data, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return map[string]any{}
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var out map[string]any
	if err := decoder.Decode(&out); err != nil || out == nil {
		return map[string]any{}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return map[string]any{}
	}
	return out
}

// Section returns one object-valued section; a missing or non-object section
// reads as nil (upstream reads it as {}).
func Section(home hivehome.Home, name string) map[string]any {
	section, _ := Load(home)[name].(map[string]any)
	return section
}

// JSONInt reports the value as an int only when it is a JSON integer: Python's
// readers compare against int, so 7 counts while 7.0, true and "7" do not.
func JSONInt(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		switch v := value.(type) {
		case int:
			return v, true
		case int64:
			return int(v), true
		}
		return 0, false
	}
	text := number.String()
	if strings.ContainsAny(text, ".eE") {
		return 0, false
	}
	parsed, err := strconv.Atoi(text)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// MaxToolResultChars is the global spillover threshold for tool results
// (loop.max_tool_result_chars), or the caller's fallback when the key is unset.
//
// Parity: framework/config.py::get_max_tool_result_chars
func MaxToolResultChars(home hivehome.Home, fallback int) int {
	if section := Section(home, "loop"); section != nil {
		if value, ok := JSONInt(section["max_tool_result_chars"]); ok {
			return value
		}
	}
	return fallback
}

// WorkerMaxContextTokens is the context window a colony worker's loop budgets
// by: worker_llm.max_context_tokens, else the worker model's catalog window,
// else — with fallback given — an explicitly pinned llm.max_context_tokens,
// else the fallback. In fallback mode the QUEEN model's catalog window is
// deliberately not consulted: a worker on an uncatalogued local model must not
// inherit a huge queen window and never compact before its real limit.
//
// Parity: framework/config.py::get_worker_max_context_tokens
func WorkerMaxContextTokens(home hivehome.Home, fallback int) int {
	if worker := Section(home, "worker_llm"); worker != nil {
		if value, ok := JSONInt(worker["max_context_tokens"]); ok {
			return value
		}
		if maxContext, ok := sectionCatalogContext(worker); ok {
			return maxContext
		}
	}
	if mainSection := Section(home, "llm"); mainSection != nil {
		if value, ok := JSONInt(mainSection["max_context_tokens"]); ok {
			return value
		}
	}
	return fallback
}

// sectionCatalogContext looks a section's provider/model pair up in the model
// catalog for its context window.
func sectionCatalogContext(section map[string]any) (int, bool) {
	provider, _ := section["provider"].(string)
	model, _ := section["model"].(string)
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if strings.EqualFold(provider, "openrouter") && strings.HasPrefix(strings.ToLower(model), "openrouter/") {
		model = model[len("openrouter/"):]
	}
	if provider == "" || model == "" {
		return 0, false
	}
	_, maxContext, ok := llm.GetModelLimits(provider, model)
	if !ok || maxContext <= 0 {
		return 0, false
	}
	return maxContext, true
}
