package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

const DefaultMockModel = "mock-model"

const mockPlainText = "This is a mock response for testing purposes."

var (
	mockOutputKeysPattern = regexp.MustCompile(`(?i)output_keys:\s*\[(.*?)\]`)
	mockKeysPattern       = regexp.MustCompile(`(?i)(?:keys|with keys):\s*([a-zA-Z0-9_,\s]+)`)
	mockSchemaAnchor      = regexp.MustCompile(`\{[^}]*"([a-zA-Z0-9_]+)":\s*`)
	mockSchemaKeyPattern  = regexp.MustCompile(`"([a-zA-Z0-9_]+)":\s*`)
)

type MockProvider struct {
	Model string
}

func NewMockProvider(model ...string) *MockProvider {
	name := DefaultMockModel
	if len(model) > 0 && model[0] != "" {
		name = model[0]
	}
	return &MockProvider{Model: name}
}

func (m *MockProvider) ModelName() string { return m.Model }

func (m *MockProvider) ExtractOutputKeys(system string) []string {
	if match := mockOutputKeysPattern.FindStringSubmatch(system); match != nil {
		parts := strings.Split(match[1], ",")
		keys := make([]string, 0, len(parts))
		for _, part := range parts {
			keys = append(keys, strings.Trim(strings.TrimSpace(part), "\"'"))
		}
		return keys
	}
	if match := mockKeysPattern.FindStringSubmatch(system); match != nil {
		parts := strings.Split(match[1], ",")
		keys := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				keys = append(keys, trimmed)
			}
		}
		return keys
	}
	if mockSchemaAnchor.MatchString(system) {
		matches := mockSchemaKeyPattern.FindAllStringSubmatch(system, -1)
		if len(matches) > 0 {
			keys := make([]string, 0, len(matches))
			seen := map[string]struct{}{}
			for _, match := range matches {
				key := match[1]
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				keys = append(keys, key)
			}
			return keys
		}
	}
	return nil
}

func (m *MockProvider) GenerateMockResponse(system string, jsonMode bool) string {
	if !jsonMode {
		return mockPlainText
	}
	keys := m.ExtractOutputKeys(system)
	if len(keys) == 0 {
		return orderedJSON([]string{"result"})
	}
	return orderedJSON(keys)
}

func orderedJSON(keys []string) string {
	var builder strings.Builder
	builder.WriteString("{\n")
	for index, key := range keys {
		builder.WriteString("  ")
		builder.WriteString(jsonString(key))
		builder.WriteString(": ")
		builder.WriteString(jsonString("mock_" + key + "_value"))
		if index != len(keys)-1 {
			builder.WriteString(",")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("}")
	return builder.String()
}

func jsonString(value string) string {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return `""`
	}
	return strings.TrimSuffix(buffer.String(), "\n")
}

func withDynamicSuffix(system, suffix string) string {
	if suffix == "" {
		return system
	}
	if system == "" {
		return suffix
	}
	return system + "\n\n" + suffix
}

func (m *MockProvider) Complete(req CompletionRequest) Completion {
	system := withDynamicSuffix(req.System, req.SystemDynamicSuffix)
	return Completion{
		Content:    m.GenerateMockResponse(system, req.JSONMode),
		Model:      m.Model,
		StopReason: "mock_complete",
	}
}

func (m *MockProvider) AComplete(_ context.Context, req CompletionRequest) (Completion, error) {
	return m.Complete(req), nil
}

func (m *MockProvider) Stream(req CompletionRequest) []StreamEvent {
	system := withDynamicSuffix(req.System, req.SystemDynamicSuffix)
	content := m.GenerateMockResponse(system, false)
	words := strings.Split(content, " ")
	accumulated := ""
	events := make([]StreamEvent, 0, len(words)+2)
	for index, word := range words {
		chunk := word
		if index != 0 {
			chunk = " " + word
		}
		accumulated += chunk
		events = append(events, TextDeltaEvent{Content: chunk, Snapshot: accumulated})
	}
	events = append(events, TextEndEvent{FullText: accumulated})
	events = append(events, FinishEvent{StopReason: "mock_complete", Model: m.Model})
	return events
}
