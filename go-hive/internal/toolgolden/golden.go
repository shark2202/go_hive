package toolgolden

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Scenario is one committed golden: the capture's canonical envelope for both
// runtimes plus how the pair was classified.
type Scenario struct {
	ID        string
	Tool      string
	Arguments map[string]any
	// Compare is the comparison mode the capture used: exact, masked, shape or
	// none (recorded only).
	Compare string
	// Status is the capture's verdict: aligned, shape-aligned, recorded or
	// different.
	Status string
	// Go is the Go-side canonical envelope captured from the running runtime.
	Go Envelope
	// Exception is the reviewed reason a pair is not compared verbatim.
	Exception string
}

type goldenDocument struct {
	Scenarios map[string]struct {
		Tool      string         `json:"tool"`
		Arguments map[string]any `json:"arguments"`
		Compare   string         `json:"compare"`
		Status    string         `json:"status"`
		Exception string         `json:"exception"`
		Python    goldenEnvelope `json:"python"`
		Go        goldenEnvelope `json:"go"`
	} `json:"scenarios"`
}

type goldenEnvelope struct {
	IsError  bool   `json:"is_error"`
	Form     string `json:"form"`
	Payload  string `json:"payload"`
	Trailer  string `json:"trailer"`
	Reminder bool   `json:"reminder"`
}

// LoadScenarios reads the committed capture report (results.json).
func LoadScenarios(path string) (map[string]Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document goldenDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("golden report %s: %w", path, err)
	}
	out := make(map[string]Scenario, len(document.Scenarios))
	for id, entry := range document.Scenarios {
		out[id] = Scenario{
			ID:        id,
			Tool:      entry.Tool,
			Arguments: entry.Arguments,
			Compare:   entry.Compare,
			Status:    entry.Status,
			Exception: entry.Exception,
			Go:        decodeEnvelope(entry.Go),
		}
	}
	return out, nil
}

func decodeEnvelope(source goldenEnvelope) Envelope {
	return FromGoldenPayload(source.IsError, source.Form, source.Payload, source.Trailer, source.Reminder)
}

// LoadFixtures reads the fixture files the capture materialised in its
// workspace (scenarios.json `fixtures`), so a test can recreate the exact
// bytes the golden was produced from.
func LoadFixtures(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var document struct {
		Fixtures map[string]string `json:"fixtures"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("scenario fixtures %s: %w", path, err)
	}
	return document.Fixtures, nil
}

// WriteFixtures materialises the fixture set in dir and returns dir.
func WriteFixtures(dir string, fixtures map[string]string) error {
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return err
		}
	}
	return nil
}
