package schemas

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Parity: framework/schemas/types.py + run.py/goal.py — Python writes
// datetime.now().isoformat() (local, naive, microsecond precision).
func TestTimestampMarshalMatchesPythonLocalISOFormat(t *testing.T) {
	local := time.Date(2026, time.November, 24, 9, 30, 15, 123456000, time.Local)
	encoded, err := json.Marshal(NewTimestamp(local))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(encoded), `"2026-11-24T09:30:15.123456"`; got != want {
		t.Fatalf("naive timestamp = %s, want %s", got, want)
	}

	// A whole second carries no fraction, exactly like isoformat().
	whole := time.Date(2026, time.November, 24, 9, 30, 15, 0, time.Local)
	encoded, err = json.Marshal(NewTimestamp(whole))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(encoded), `"2026-11-24T09:30:15"`; got != want {
		t.Fatalf("whole-second timestamp = %s, want %s", got, want)
	}

	// An aware value keeps its offset, as datetime.isoformat() does.
	aware := time.Date(2026, time.November, 24, 1, 30, 15, 0, time.UTC)
	encoded, err = json.Marshal(NewTimestamp(aware))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(encoded), `"2026-11-24T01:30:15+00:00"`; got != want {
		t.Fatalf("aware timestamp = %s, want %s", got, want)
	}
}

// Parity: datetime.fromisoformat — a naive string is local time, not UTC.
func TestParseISOTimeTreatsNaiveAsLocal(t *testing.T) {
	parsed, err := ParseISOTime("2026-11-24T09:30:15.123456")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := time.Date(2026, time.November, 24, 9, 30, 15, 123456000, time.Local)
	if !parsed.Equal(want) || parsed.Location() != time.Local {
		t.Fatalf("parsed = %v (%v), want %v (local)", parsed, parsed.Location(), want)
	}
	zoned, err := ParseISOTime("2026-11-24T01:30:15Z")
	if err != nil {
		t.Fatalf("parse zoned: %v", err)
	}
	wantZoned := time.Date(2026, time.November, 24, 1, 30, 15, 0, time.UTC)
	if !zoned.Equal(wantZoned) || zoned.Location() != time.UTC {
		t.Fatalf("zoned = %v (%v), want %v (UTC)", zoned, zoned.Location(), wantZoned)
	}
}

// Parity: pydantic required fields.
func TestRequiredFieldsRejectedLikePydantic(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		input string
		parse func([]byte) error
	}{
		{"goal missing description", "goal", `{"id":"g","name":"n"}`, func(b []byte) error {
			var goal Goal
			return json.Unmarshal(b, &goal)
		}},
		{"criterion missing metric", "success criterion", `{"id":"c","description":"d","target":""}`, func(b []byte) error {
			var criterion SuccessCriterion
			return json.Unmarshal(b, &criterion)
		}},
		{"criterion missing target", "success criterion", `{"id":"c","description":"d","metric":"llm_judge"}`, func(b []byte) error {
			var criterion SuccessCriterion
			return json.Unmarshal(b, &criterion)
		}},
		{"constraint missing id", "constraint", `{"description":"d","constraint_type":"hard"}`, func(b []byte) error {
			var constraint Constraint
			return json.Unmarshal(b, &constraint)
		}},
		{"option missing action_type", "option", `{"id":"o","description":"d"}`, func(b []byte) error {
			var option Option
			return json.Unmarshal(b, &option)
		}},
		{"outcome missing success", "outcome", `{"result":1}`, func(b []byte) error {
			var outcome Outcome
			return json.Unmarshal(b, &outcome)
		}},
		{"decision missing node_id", "decision", `{"id":"d","intent":"i"}`, func(b []byte) error {
			var decision Decision
			return json.Unmarshal(b, &decision)
		}},
		{"problem missing severity", "problem", `{"id":"p","description":"d"}`, func(b []byte) error {
			var problem Problem
			return json.Unmarshal(b, &problem)
		}},
		{"run missing goal_id", "run", `{"id":"r"}`, func(b []byte) error {
			var run Run
			return json.Unmarshal(b, &run)
		}},
		{"agent config missing entry_node", "agent config",
			`{"name":"a","goal":{"description":"d"},"nodes":[],"edges":[]}`, func(b []byte) error {
				var config AgentConfig
				return json.Unmarshal(b, &config)
			}},
		{"node missing id", "node", `{"max_iterations":1}`, func(b []byte) error {
			var node NodeConfig
			return json.Unmarshal(b, &node)
		}},
		{"edge missing to_node", "edge", `{"from_node":"a"}`, func(b []byte) error {
			var edge EdgeConfig
			return json.Unmarshal(b, &edge)
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.parse([]byte(testCase.input))
			if err == nil {
				t.Fatalf("accepted an object missing a required %s field", testCase.kind)
			}
			if !strings.Contains(err.Error(), testCase.kind) {
				t.Fatalf("error = %v, want it to name %q", err, testCase.kind)
			}
		})
	}
}

// Parity: pydantic field defaults (0.5 / 30 / 1 / 1 / 4096) apply only when the
// key is absent; an explicit 0 stays 0.
func TestExplicitZeroIsNotNormalised(t *testing.T) {
	var option Option
	if err := json.Unmarshal([]byte(`{"id":"o","description":"d","action_type":"generate","confidence":0}`), &option); err != nil {
		t.Fatalf("unmarshal option: %v", err)
	}
	if option.Confidence != 0 || option.EffectiveConfidence() != 0 {
		t.Fatalf("explicit confidence 0 became %v", option.EffectiveConfidence())
	}
	encoded, _ := json.Marshal(option)
	if !strings.Contains(string(encoded), `"confidence":0`) {
		t.Fatalf("option JSON = %s, want explicit confidence 0", encoded)
	}
	var defaulted Option
	if err := json.Unmarshal([]byte(`{"id":"o","description":"d","action_type":"generate"}`), &defaulted); err != nil {
		t.Fatalf("unmarshal default option: %v", err)
	}
	if defaulted.EffectiveConfidence() != 0.5 {
		t.Fatalf("missing confidence = %v, want the schema default 0.5", defaulted.EffectiveConfidence())
	}

	configJSON := `{
		"name":"a","goal":{"description":"d"},
		"nodes":[{"id":"n","max_iterations":0,"max_node_visits":0}],
		"edges":[{"from_node":"n","to_node":"n","priority":0}],
		"entry_node":"n","max_tokens":0
	}`
	var config AgentConfig
	if err := json.Unmarshal([]byte(configJSON), &config); err != nil {
		t.Fatalf("unmarshal agent config: %v", err)
	}
	if config.MaxTokens != 0 || config.Nodes[0].MaxIterations != 0 || config.Nodes[0].MaxNodeVisits != 0 || config.Edges[0].Priority != 0 {
		t.Fatalf("explicit zeros normalised: config=%+v node=%+v edge=%+v", config, config.Nodes[0], config.Edges[0])
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal agent config: %v", err)
	}
	for _, want := range []string{`"max_tokens":0`, `"max_iterations":0`, `"max_node_visits":0`, `"priority":0`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("marshalled config %s, want %s", encoded, want)
		}
	}

	var defaultedConfig AgentConfig
	if err := json.Unmarshal([]byte(`{"name":"a","goal":{"description":"d"},"nodes":[{"id":"n"}],"edges":[{"from_node":"n","to_node":"n"}],"entry_node":"n"}`), &defaultedConfig); err != nil {
		t.Fatalf("unmarshal default config: %v", err)
	}
	if defaultedConfig.MaxTokens != 4096 || defaultedConfig.Nodes[0].MaxIterations != 30 ||
		defaultedConfig.Nodes[0].MaxNodeVisits != 1 || defaultedConfig.Edges[0].Priority != 1 {
		t.Fatalf("defaults = max_tokens:%d iterations:%d visits:%d priority:%d",
			defaultedConfig.MaxTokens, defaultedConfig.Nodes[0].MaxIterations,
			defaultedConfig.Nodes[0].MaxNodeVisits, defaultedConfig.Edges[0].Priority)
	}
}

// Parity: run.py declares started_at with default_factory=datetime.now, so a
// missing key serialises as a timestamp, never null.
func TestRunStartedAtDefaultsToNow(t *testing.T) {
	before := time.Now().Add(-time.Second)
	var run Run
	if err := json.Unmarshal([]byte(`{"id":"r","goal_id":"g"}`), &run); err != nil {
		t.Fatalf("unmarshal run: %v", err)
	}
	if run.StartedAt.IsZero() {
		t.Fatal("started_at was left zero")
	}
	if run.StartedAt.Time.Before(before) || run.StartedAt.Time.After(time.Now().Add(time.Second)) {
		t.Fatalf("started_at = %v, want the current time", run.StartedAt.Time)
	}
	encoded, err := json.Marshal(run)
	if err != nil {
		t.Fatalf("marshal run: %v", err)
	}
	if strings.Contains(string(encoded), `"started_at":null`) {
		t.Fatalf("run JSON = %s, want a concrete started_at", encoded)
	}
}
