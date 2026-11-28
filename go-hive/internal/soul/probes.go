package soul

// Parity: framework/agents/queen/soul_probes.py

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// ProbesFile holds the fixed questions put to a queen, before and after a
// proposal's changes. It sits in her soul folder, beside soul.json.
const ProbesFile = "probes.json"

// ProbeQuestion is one entry of probes.json.
type ProbeQuestion struct {
	Q      string
	Anchor string
}

// ProbeAnswerFunc asks the queen's model one question under one system
// prompt. The route supplies it from the queen's configured model.
type ProbeAnswerFunc func(system, question string) (string, error)

// ProbeOptions is one probe run: the proposal whose edits are compared with
// the soul as it stands now.
type ProbeOptions struct {
	QueensDir string
	QueenID   string
	Config    *SoulConfig
	Proposal  *Proposal
	// Identity is her rendered identity prompt; an empty value leaves the
	// soul alone as the system prompt, exactly like upstream.
	Identity string
	// MemoryDirs are the folders live recall reads, in order: her own scope,
	// then global. A question that names a speaker gets that speaker's memory
	// files appended, so the probe meets them as recall would, not as
	// strangers.
	MemoryDirs []string
	Answer     ProbeAnswerFunc
}

var (
	probingMu sync.Mutex
	probing   = map[string]bool{}
)

// IsProbing reports whether a probe is answering this queen's questions.
func IsProbing(queenID string) bool {
	probingMu.Lock()
	defer probingMu.Unlock()
	return probing[queenID]
}

// TryBeginProbing claims the queen's probe slot. It returns false when a probe
// is already running, so two POSTs cannot answer the same proposal twice.
func TryBeginProbing(queenID string) bool {
	probingMu.Lock()
	defer probingMu.Unlock()
	if probing[queenID] {
		return false
	}
	probing[queenID] = true
	return true
}

// EndProbing releases the queen's probe slot.
func EndProbing(queenID string) {
	probingMu.Lock()
	defer probingMu.Unlock()
	delete(probing, queenID)
}

// LoadProbes reads probes.json. Missing or malformed files read as no
// questions; entries without a question are skipped.
//
// Parity: framework/agents/queen/soul_probes.py::load_probes
func LoadProbes(queensDir, queenID string) []ProbeQuestion {
	data := readJSONFile(filepath.Join(SoulDir(queensDir, queenID), ProbesFile))
	list, ok := data.([]any)
	if !ok {
		return nil
	}
	probes := make([]ProbeQuestion, 0, len(list))
	for _, entry := range list {
		mapping, isMap := entry.(map[string]any)
		if !isMap {
			continue
		}
		question := probeText(mapping["q"])
		if question == "" {
			continue
		}
		probes = append(probes, ProbeQuestion{Q: question, Anchor: probeText(mapping["anchor"])})
	}
	return probes
}

func probeText(value any) string {
	if pyFalsy(value) {
		return ""
	}
	return strings.TrimSpace(PyString(value))
}

func probeSystem(identity string, cfg *SoulConfig, current *Soul) string {
	if cfg == nil {
		return identity
	}
	rendered := Render(cfg, current)
	if rendered == "" {
		return identity
	}
	if identity == "" {
		return rendered
	}
	return identity + "\n\n" + rendered
}

// probeSpeakerIDRe finds the speaker ids a question carries: "[Ann(12345)]:"
// headers, the same detection live recall uses to bring that person's memory
// files beside the message.
var probeSpeakerIDRe = regexp.MustCompile(`\[[^\[\]\n]*?\((\d{5,})\)\]:`)

// probeRelatedMemories is how many of her memory files a probe brings, the
// same cap as recall.
const probeRelatedMemories = 3

// RelatedMemories returns the memory files that mention a speaker id in the
// question, formatted as recall injects them: at most three, her scope before
// global, each headed by its file name. A question without a speaker id brings
// nothing, so a stranger stays a stranger.
//
// Parity: framework/agents/queen/soul_probes.py::related_memories
func RelatedMemories(memoryDirs []string, question string) string {
	matches := probeSpeakerIDRe.FindAllStringSubmatch(question, -1)
	if len(matches) == 0 {
		return ""
	}
	found := make([]string, 0, probeRelatedMemories)
	for _, dir := range memoryDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
				continue
			}
			data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
			if readErr != nil {
				continue
			}
			text := strings.ToValidUTF8(string(data), "\uFFFD")
			if !mentionsAny(text, matches) {
				continue
			}
			found = append(found, "### "+entry.Name()+"\n"+strings.TrimSpace(text))
			if len(found) >= probeRelatedMemories {
				break
			}
		}
		if len(found) >= probeRelatedMemories {
			break
		}
	}
	if len(found) == 0 {
		return ""
	}
	return "<recalled_memories>\n" + strings.Join(found, "\n\n") + "\n</recalled_memories>"
}

func mentionsAny(text string, matches [][]string) bool {
	for _, match := range matches {
		if len(match) > 1 && strings.Contains(text, match[1]) {
			return true
		}
	}
	return false
}

func withRelatedMemories(system, memories string) string {
	if memories == "" {
		return system
	}
	return system + "\n\n" + memories
}

// Probe answers every question twice: with her soul as it stands, and with
// the proposal's edits applied. Answers are stored in the proposal so the
// dashboard can show them side by side; a person judges them.
//
// Parity: framework/agents/queen/soul_probes.py::probe
func Probe(opts ProbeOptions) ([]map[string]any, error) {
	if opts.Proposal == nil {
		return nil, errors.New("soul: no proposal to probe")
	}
	if opts.Answer == nil {
		return nil, errors.New("soul: no probe answer function")
	}
	probes := LoadProbes(opts.QueensDir, opts.QueenID)
	if len(probes) == 0 {
		return []map[string]any{}, nil
	}

	current := Load(opts.QueensDir, opts.QueenID)
	sections, err := ApplyEdits(current, opts.Proposal.Edits)
	if err != nil {
		return nil, err
	}
	after := &Soul{Version: current.Version + 1, Sections: sections}
	beforeSystem := probeSystem(opts.Identity, opts.Config, current)
	afterSystem := probeSystem(opts.Identity, opts.Config, after)

	results := make([]map[string]any, 0, len(probes))
	for _, probe := range probes {
		memories := RelatedMemories(opts.MemoryDirs, probe.Q)
		before := answerProbe(opts.Answer, withRelatedMemories(beforeSystem, memories), probe.Q)
		afterAnswer := answerProbe(opts.Answer, withRelatedMemories(afterSystem, memories), probe.Q)
		results = append(results, map[string]any{
			"q":      probe.Q,
			"anchor": probe.Anchor,
			"before": before,
			"after":  afterAnswer,
		})
	}

	fresh := LoadProposal(opts.QueensDir, opts.QueenID, opts.Proposal.ID)
	if fresh == nil {
		fresh = opts.Proposal
	}
	fresh.Probes = results
	if err := SaveProposal(opts.QueensDir, opts.QueenID, fresh); err != nil {
		return results, err
	}
	return results, nil
}

func answerProbe(answer ProbeAnswerFunc, system, question string) string {
	reply, err := answer(system, question)
	if err != nil {
		return "(failed: " + err.Error() + ")"
	}
	return strings.TrimSpace(reply)
}
