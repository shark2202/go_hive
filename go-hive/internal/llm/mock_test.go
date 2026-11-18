package llm

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

// Parity: core/tests/test_litellm_provider.py::test_mock_provider_acomplete
func TestMockProviderAComplete(t *testing.T) {
	provider := NewMockProvider()
	result, err := provider.AComplete(context.Background(), CompletionRequest{
		Messages: []ChatMessage{{Role: "user", Content: "test"}},
		System:   "Be helpful.",
	})
	if err != nil {
		t.Fatalf("acomplete: %v", err)
	}
	if result.Content == "" {
		t.Error("content should be non-empty")
	}
	if result.Model != "mock-model" {
		t.Errorf("model = %q, want mock-model", result.Model)
	}
}

// Parity-exempt: no Python test module covers MockLLMProvider's internals.
func TestMockCompletePlainText(t *testing.T) {
	provider := NewMockProvider("mock-model")
	result := provider.Complete(CompletionRequest{})
	want := Completion{
		Content:    "This is a mock response for testing purposes.",
		Model:      "mock-model",
		StopReason: "mock_complete",
	}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("completion = %+v, want %+v", result, want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockJSONModeFallback(t *testing.T) {
	provider := NewMockProvider()
	got := provider.GenerateMockResponse("Be helpful.", true)
	want := "{\n  \"result\": \"mock_result_value\"\n}"
	if got != want {
		t.Errorf("json fallback = %q, want %q", got, want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockExtractsOutputKeysBracket(t *testing.T) {
	provider := NewMockProvider()
	system := `output_keys: [ "name" , 'age' ]`
	keys := provider.ExtractOutputKeys(system)
	if want := []string{"name", "age"}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	got := provider.GenerateMockResponse(system, true)
	want := "{\n  \"name\": \"mock_name_value\",\n  \"age\": \"mock_age_value\"\n}"
	if got != want {
		t.Errorf("json = %q, want %q", got, want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockExtractsKeysPhrase(t *testing.T) {
	provider := NewMockProvider()
	system := "Generate JSON with keys: name, age"
	if want := []string{"name", "age"}; !reflect.DeepEqual(provider.ExtractOutputKeys(system), want) {
		t.Errorf("keys = %v, want %v", provider.ExtractOutputKeys(system), want)
	}
	got := provider.GenerateMockResponse(system, true)
	want := "{\n  \"name\": \"mock_name_value\",\n  \"age\": \"mock_age_value\"\n}"
	if got != want {
		t.Errorf("json = %q, want %q", got, want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockExtractsSchemaKeys(t *testing.T) {
	provider := NewMockProvider()
	system := `Return {"alpha": 1, "beta": "x"}`
	if want := []string{"alpha", "beta"}; !reflect.DeepEqual(provider.ExtractOutputKeys(system), want) {
		t.Errorf("keys = %v, want %v", provider.ExtractOutputKeys(system), want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockOutputKeysPatternWins(t *testing.T) {
	provider := NewMockProvider()
	system := "output_keys: [first]\nkeys: second"
	if want := []string{"first"}; !reflect.DeepEqual(provider.ExtractOutputKeys(system), want) {
		t.Errorf("keys = %v, want %v", provider.ExtractOutputKeys(system), want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockEmptyOutputKeysListKeepsEmptyKey(t *testing.T) {
	provider := NewMockProvider()
	keys := provider.ExtractOutputKeys("output_keys: []")
	if want := []string{""}; !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockStreamWordByWord(t *testing.T) {
	provider := NewMockProvider("mock-model")
	events := provider.Stream(CompletionRequest{})
	words := strings.Split("This is a mock response for testing purposes.", " ")
	if len(events) != len(words)+2 {
		t.Fatalf("events = %d, want %d", len(events), len(words)+2)
	}
	accumulated := ""
	for index, word := range words {
		delta, ok := events[index].(TextDeltaEvent)
		if !ok {
			t.Fatalf("event %d = %T, want TextDeltaEvent", index, events[index])
		}
		wantChunk := word
		if index != 0 {
			wantChunk = " " + word
		}
		if delta.Content != wantChunk {
			t.Errorf("event %d content = %q, want %q", index, delta.Content, wantChunk)
		}
		accumulated += wantChunk
		if delta.Snapshot != accumulated {
			t.Errorf("event %d snapshot = %q, want %q", index, delta.Snapshot, accumulated)
		}
	}
	end, ok := events[len(words)].(TextEndEvent)
	if !ok {
		t.Fatalf("end event = %T", events[len(words)])
	}
	if end.FullText != accumulated {
		t.Errorf("full_text = %q, want %q", end.FullText, accumulated)
	}
	finish, ok := events[len(words)+1].(FinishEvent)
	if !ok {
		t.Fatalf("finish event = %T", events[len(words)+1])
	}
	if finish.StopReason != "mock_complete" || finish.Model != "mock-model" {
		t.Errorf("finish = %+v", finish)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockDynamicSuffixIsAppended(t *testing.T) {
	provider := NewMockProvider()
	req := CompletionRequest{
		System:              "output_keys: [name]",
		SystemDynamicSuffix: "keys: age",
		JSONMode:            true,
	}
	result := provider.Complete(req)
	want := "{\n  \"name\": \"mock_name_value\"\n}"
	if result.Content != want {
		t.Errorf("content = %q, want %q", result.Content, want)
	}

	req = CompletionRequest{SystemDynamicSuffix: "output_keys: [solo]", JSONMode: true}
	result = provider.Complete(req)
	want = "{\n  \"solo\": \"mock_solo_value\"\n}"
	if result.Content != want {
		t.Errorf("suffixed-only content = %q, want %q", result.Content, want)
	}
}

// Parity-exempt: see TestMockCompletePlainText.
func TestMockDefaultModelIgnoresEmptyOverride(t *testing.T) {
	if got := NewMockProvider("").ModelName(); got != "mock-model" {
		t.Errorf("model = %q, want mock-model", got)
	}
	if got := NewMockProvider("custom").ModelName(); got != "custom" {
		t.Errorf("model = %q, want custom", got)
	}
}
