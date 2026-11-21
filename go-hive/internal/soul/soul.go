package soul

// Parity: framework/agents/queen/soul.py::Soul

import (
	"encoding/json"
	"strings"
)

type Item struct {
	Text string `json:"text"`
	Date string `json:"date"`
}

type Soul struct {
	Version   int               `json:"version"`
	UpdatedAt string            `json:"updated_at"`
	Sections  map[string][]Item `json:"sections"`
}

type Edit struct {
	Op       string           `json:"op"`
	Section  string           `json:"section"`
	Text     string           `json:"text"`
	Old      string           `json:"old"`
	Date     string           `json:"date"`
	Evidence []map[string]any `json:"evidence"`
	Reason   string           `json:"reason"`
	Expired  bool             `json:"expired"`
}

type Proposal struct {
	ID          string           `json:"id"`
	CreatedAt   string           `json:"created_at"`
	BaseVersion int              `json:"base_version"`
	Edits       []Edit           `json:"edits"`
	Summary     string           `json:"summary"`
	Status      string           `json:"status"`
	Applied     []int            `json:"applied"`
	Sources     []string         `json:"sources"`
	Bootstrap   bool             `json:"bootstrap"`
	Probes      []map[string]any `json:"probes"`
}

type SoulError struct{ Message string }

func (e *SoulError) Error() string { return e.Message }

func (s *Soul) Items(key string) []Item {
	if s == nil || s.Sections == nil {
		return nil
	}
	return s.Sections[key]
}

func (s *Soul) IsEmpty() bool {
	if s == nil {
		return true
	}
	for _, items := range s.Sections {
		if len(items) > 0 {
			return false
		}
	}
	return true
}

func SoulFromJSON(value any) *Soul {
	data, ok := value.(map[string]any)
	if !ok {
		data = map[string]any{}
	}
	sections := map[string][]Item{}
	if raw, isMap := data["sections"].(map[string]any); isMap {
		for key, rawItems := range raw {
			list, _ := rawItems.([]any)
			items := make([]Item, 0, len(list))
			for _, entry := range list {
				mapping, isItem := entry.(map[string]any)
				if !isItem {
					continue
				}
				text := strings.TrimSpace(PyString(getDefault(mapping, "text", "")))
				if text == "" {
					continue
				}
				items = append(items, Item{Text: text, Date: PyString(getDefault(mapping, "date", ""))})
			}
			sections[key] = items
		}
	}
	version := 0
	if parsed, ok := AsInt(data["version"]); ok {
		version = parsed
	}
	return &Soul{Version: version, UpdatedAt: PyString(getDefault(data, "updated_at", "")), Sections: sections}
}

func getDefault(mapping map[string]any, key string, fallback any) any {
	if value, present := mapping[key]; present {
		return value
	}
	return fallback
}

func ProposalFromJSON(value map[string]any) *Proposal {
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var proposal Proposal
	if err := json.Unmarshal(data, &proposal); err != nil {
		return nil
	}
	proposal.normalize(value)
	return &proposal
}

func (p *Proposal) normalize(source map[string]any) {
	if _, present := source["status"]; !present {
		p.Status = "pending"
	}
	p.normalizeShape()
}

// normalizeShape fills the list fields with empty slices, the same shape the
// Python dataclass defaults write: a not-yet-decided proposal persists
// "applied": [] and "probes": [], not Go's null.
func (p *Proposal) normalizeShape() {
	if p.Edits == nil {
		p.Edits = []Edit{}
	}
	for i := range p.Edits {
		if p.Edits[i].Evidence == nil {
			p.Edits[i].Evidence = []map[string]any{}
		}
	}
	if p.Applied == nil {
		p.Applied = []int{}
	}
	if p.Sources == nil {
		p.Sources = []string{}
	}
	if p.Probes == nil {
		p.Probes = []map[string]any{}
	}
}
