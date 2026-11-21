package soul

// Parity: framework/agents/queen/soul.py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const SoulFile = "soul.json"

func SoulDir(queensDir, queenID string) string {
	return filepath.Join(queensDir, queenID, "soul")
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	handle, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := handle.Name()
	fail := func(cause error) error {
		handle.Close()
		os.Remove(tempPath)
		return cause
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fail(err)
	}
	if _, err := handle.Write(bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))); err != nil {
		return fail(err)
	}
	if err := handle.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return err
	}
	return nil
}

func readJSONFile(path string) any {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil
	}
	return value
}

func Load(queensDir, queenID string) *Soul {
	return SoulFromJSON(readJSONFile(filepath.Join(SoulDir(queensDir, queenID), SoulFile)))
}

func LoadVersion(queensDir, queenID string, version int) *Soul {
	data := readJSONFile(filepath.Join(SoulDir(queensDir, queenID), "history", fmt.Sprintf("v%d.json", version)))
	if data == nil {
		return nil
	}
	return SoulFromJSON(data)
}

func SectionChars(items []Item) int {
	total := 0
	for _, item := range items {
		total += len([]rune(item.Text))
	}
	return total
}

func Problems(cfg *SoulConfig, soul *Soul) []string {
	out := make([]string, 0)
	for _, section := range cfg.Sections {
		used := SectionChars(soul.Items(section.Key))
		if used > section.Limit {
			out = append(out, fmt.Sprintf("%s: %d/%d characters", section.Title, used, section.Limit))
		}
	}
	return out
}

func NowISO(now time.Time) string {
	return now.Format("2006-01-02T15:04:05-07:00")
}

type logEntry struct {
	Version int    `json:"version"`
	At      string `json:"at"`
	Author  string `json:"author"`
	Note    string `json:"note"`
	Edits   []Edit `json:"edits"`
}

func pythonJSONDumps(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	return spaceJSON(data), nil
}

func spaceJSON(data []byte) []byte {
	out := make([]byte, 0, len(data)+len(data)/8)
	inString := false
	escaped := false
	for _, b := range data {
		out = append(out, b)
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
				continue
			}
			if b == '"' {
				inString = false
			}
			continue
		}
		if b == '"' {
			inString = true
			continue
		}
		if b == ',' || b == ':' {
			out = append(out, ' ')
		}
	}
	return out
}

func Save(queensDir string, cfg *SoulConfig, queenID string, sections map[string][]Item, author, note string, edits []Edit) (*Soul, error) {
	current := Load(queensDir, queenID)
	soul := &Soul{Version: current.Version + 1, UpdatedAt: NowISO(time.Now()), Sections: sections}
	if found := Problems(cfg, soul); len(found) > 0 {
		return nil, &SoulError{Message: strings.Join(found, "; ")}
	}
	root := SoulDir(queensDir, queenID)
	if err := writeJSONFile(filepath.Join(root, "history", fmt.Sprintf("v%d.json", soul.Version)), soul); err != nil {
		return nil, err
	}
	if err := writeJSONFile(filepath.Join(root, SoulFile), soul); err != nil {
		return nil, err
	}
	if edits == nil {
		edits = []Edit{}
	}
	line, err := pythonJSONDumps(logEntry{Version: soul.Version, At: soul.UpdatedAt, Author: author, Note: note, Edits: edits})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	handle, err := os.OpenFile(filepath.Join(root, "log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := handle.Write(append(line, '\n')); err != nil {
		handle.Close()
		return nil, err
	}
	if err := handle.Close(); err != nil {
		return nil, err
	}
	return soul, nil
}

func History(queensDir, queenID string) []map[string]any {
	path := filepath.Join(SoulDir(queensDir, queenID), "log.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	entries := make([]map[string]any, 0)
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		decoder := json.NewDecoder(strings.NewReader(line))
		decoder.UseNumber()
		var entry map[string]any
		if err := decoder.Decode(&entry); err != nil || entry == nil {
			continue
		}
		edits, _ := entry["edits"].([]any)
		entries = append(entries, map[string]any{
			"version": entry["version"],
			"at":      entry["at"],
			"author":  entry["author"],
			"note":    entry["note"],
			"changes": len(edits),
		})
	}
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	return entries
}

func Rollback(queensDir string, cfg *SoulConfig, queenID string, version int, author string) (*Soul, error) {
	old := LoadVersion(queensDir, queenID, version)
	if old == nil {
		return nil, &SoulError{Message: fmt.Sprintf("there is no version %d", version)}
	}
	return Save(queensDir, cfg, queenID, old.Sections, author, fmt.Sprintf("back to version %d", version), nil)
}

func SaveProposal(queensDir, queenID string, proposal *Proposal) error {
	if proposal == nil {
		return &SoulError{Message: "soul: no proposal to save"}
	}
	proposal.normalizeShape()
	return writeJSONFile(filepath.Join(SoulDir(queensDir, queenID), "proposals", proposal.ID+".json"), proposal)
}

func LoadProposal(queensDir, queenID, proposalID string) *Proposal {
	if strings.Contains(proposalID, "/") || strings.Contains(proposalID, "\\") || strings.HasPrefix(proposalID, ".") {
		return nil
	}
	data := readJSONFile(filepath.Join(SoulDir(queensDir, queenID), "proposals", proposalID+".json"))
	mapping, ok := data.(map[string]any)
	if !ok {
		return nil
	}
	return ProposalFromJSON(mapping)
}

func PendingProposal(queensDir, queenID string) *Proposal {
	folder := filepath.Join(SoulDir(queensDir, queenID), "proposals")
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for i := len(names) - 1; i >= 0; i-- {
		proposalID := strings.TrimSuffix(names[i], ".json")
		proposal := LoadProposal(queensDir, queenID, proposalID)
		if proposal != nil && proposal.Status == "pending" {
			return proposal
		}
	}
	return nil
}

func ApplyEdits(soul *Soul, edits []Edit) (map[string][]Item, error) {
	sections := make(map[string][]Item, len(soul.Sections))
	for key, items := range soul.Sections {
		copied := make([]Item, len(items))
		copy(copied, items)
		sections[key] = copied
	}
	order := make([]int, len(edits))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool {
		return !(edits[order[a]].Op == "add") && edits[order[b]].Op == "add"
	})
	for _, n := range order {
		edit := edits[n]
		items := sections[edit.Section]
		if edit.Op == "add" {
			sections[edit.Section] = append(items, Item{Text: strings.TrimSpace(edit.Text), Date: edit.Date})
			continue
		}
		at := -1
		for i, item := range items {
			if item.Text == edit.Old {
				at = i
				break
			}
		}
		if at < 0 {
			return nil, &SoulError{Message: fmt.Sprintf("change %d no longer applies: its item has been changed or removed since", n+1)}
		}
		if edit.Op == "remove" {
			sections[edit.Section] = append(items[:at], items[at+1:]...)
		} else {
			date := edit.Date
			if date == "" {
				date = items[at].Date
			}
			items[at] = Item{Text: strings.TrimSpace(edit.Text), Date: date}
		}
	}
	return sections, nil
}

func Decide(queensDir string, cfg *SoulConfig, queenID string, proposal *Proposal, accept *[]int, author string) (*Soul, error) {
	chosen := make([]int, 0, len(proposal.Edits))
	if accept == nil {
		for i := range proposal.Edits {
			chosen = append(chosen, i)
		}
	} else {
		seen := map[int]struct{}{}
		for _, index := range *accept {
			if index < 0 || index >= len(proposal.Edits) {
				continue
			}
			if _, exists := seen[index]; exists {
				continue
			}
			seen[index] = struct{}{}
			chosen = append(chosen, index)
		}
		sort.Ints(chosen)
	}
	var soul *Soul
	if len(chosen) > 0 {
		edits := make([]Edit, 0, len(chosen))
		for _, index := range chosen {
			edits = append(edits, proposal.Edits[index])
		}
		what := "reflection " + proposal.ID
		if proposal.Bootstrap {
			what = "first draft"
		}
		sections, err := ApplyEdits(Load(queensDir, queenID), edits)
		if err != nil {
			return nil, err
		}
		soul, err = Save(queensDir, cfg, queenID, sections, author,
			fmt.Sprintf("%s: %d of %d changes", what, len(edits), len(proposal.Edits)), edits)
		if err != nil {
			return nil, err
		}
	}
	if len(chosen) > 0 {
		proposal.Status = "applied"
	} else {
		proposal.Status = "dismissed"
	}
	proposal.Applied = chosen
	if err := SaveProposal(queensDir, queenID, proposal); err != nil {
		return nil, err
	}
	return soul, nil
}

func Render(cfg *SoulConfig, soul *Soul) string {
	if soul.IsEmpty() {
		return ""
	}
	lines := []string{"<soul>", cfg.Header}
	for _, section := range cfg.Sections {
		items := soul.Items(section.Key)
		if len(items) == 0 {
			continue
		}
		lines = append(lines, "", "## "+section.Title)
		for _, item := range items {
			line := "- " + item.Text
			if item.Date != "" {
				line += " (" + ShortDate(item.Date) + ")"
			}
			lines = append(lines, line)
		}
	}
	lines = append(lines, "</soul>")
	return strings.Join(lines, "\n")
}

func ShortDate(day string) string {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return fmt.Sprintf("%d/%d", int(parsed.Month()), parsed.Day())
}

func PromptFor(config map[string]any, queensDir, queenID string) string {
	if queenID == "" {
		return ""
	}
	cfg := GetSoulConfig(config, queenID)
	if cfg == nil {
		return ""
	}
	return Render(cfg, Load(queensDir, queenID))
}
