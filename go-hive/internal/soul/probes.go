package soul

// Parity: framework/agents/queen/soul_probes.py

import (
	"errors"
	"path/filepath"
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
	Answer   ProbeAnswerFunc
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
		before := answerProbe(opts.Answer, beforeSystem, probe.Q)
		afterAnswer := answerProbe(opts.Answer, afterSystem, probe.Q)
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
