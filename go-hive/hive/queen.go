package hive

import (
	"github.com/shark2202/go_hive/go-hive/internal/queen"
)

// Queen is the identifying view of one queen profile.
//
// Parity: framework/agents/queen/queen_profiles.py::list_queens
type Queen struct {
	ID        string
	Name      string
	Title     string
	Custom    bool
	HasAvatar bool
}

// QueenProfile is a queen's persona plus the capability defaults the runtime
// reads from it.
//
// Persona is a copy of the profile with the capability keys removed; it is
// the same data a queen's system prompt is built from and carries no
// credentials.
//
// Parity: framework/agents/queen/queen_profiles.py::load_queen_profile
type QueenProfile struct {
	Queen
	ToolCategories []string
	PresetSkills   []string
	Persona        map[string]any
}

// ListQueens lists the built-in and on-disk queen profiles, sorted by id.
//
// Parity: framework/agents/queen/queen_profiles.py::list_queens
func ListQueens(home Home) []Queen {
	if home.Root == "" {
		return nil
	}
	entries := queen.ListQueens(home.QueensDir())
	out := make([]Queen, 0, len(entries))
	for _, entry := range entries {
		out = append(out, queenFromMap(entry))
	}
	return out
}

// LoadQueenProfile loads one queen's profile, materialising a built-in default
// when the queen has no on-disk copy yet.
//
// Parity: framework/agents/queen/queen_profiles.py::load_queen_profile
func LoadQueenProfile(home Home, queenID string) (QueenProfile, error) {
	profile, err := queen.LoadQueenProfile(home.QueensDir(), queenID)
	if err != nil {
		return QueenProfile{}, err
	}
	entry := Queen{
		ID:        queenID,
		Name:      stringValue(profile["name"]),
		Title:     stringValue(profile["title"]),
		Custom:    !queen.IsDefaultQueen(queenID),
		HasAvatar: queen.QueenHasAvatar(home.QueensDir(), queenID),
	}
	tools := stringSlice(profile["default_tool_categories"])
	preset := queen.PresetSkillsFromProfile(profile)
	if !entry.Custom {
		// A materialised default profile deliberately keeps the persona only,
		// so its capability keys come from the embedded definition.
		tools = queen.DefaultToolsFor(queenID)
		preset = queen.DefaultSkillsFor(queenID)
	}
	return QueenProfile{
		Queen:          entry,
		ToolCategories: tools,
		PresetSkills:   sortedKeys(preset),
		Persona:        queen.PersonaOnly(profile),
	}, nil
}

func queenFromMap(entry map[string]any) Queen {
	return Queen{
		ID:        stringValue(entry["id"]),
		Name:      stringValue(entry["name"]),
		Title:     stringValue(entry["title"]),
		Custom:    boolValue(entry["custom"]),
		HasAvatar: boolValue(entry["has_avatar"]),
	}
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func boolValue(value any) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return false
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if text, ok := entry.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func sortedKeys(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sortStrings(out)
	return out
}
