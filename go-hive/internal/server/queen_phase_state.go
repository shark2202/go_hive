package server

// Parity: framework/tools/queen_lifecycle_tools.py::QueenPhaseState
//
// The Python QueenPhaseState owns the queen's operating phase, its tool tiers
// and prompts, and the meta.json copy every transition writes. This port keeps
// the phase slot: the two phases, the persistence cascade
// (explicit initial_phase > meta.json["phase"] > physical colony bindings >
// independent), and the QUEEN_PHASE_CHANGED emission. Session.PhaseState was
// declared but never assigned, so every phase-aware route (live tools, the
// queen tool-allowlist refresh, the session payload's queen_phase) saw nil.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/tools"
)

const (
	// QueenPhaseIndependent is the standalone queen: MCP tools, no colony
	// workers.
	QueenPhaseIndependent = "independent"
	// QueenPhaseColony is the colony-bound queen: workers may run and the
	// queen monitors, intervenes and fans out follow-up runs.
	QueenPhaseColony = "colony"

	sessionPhaseMetaKey = "phase"
)

// normalizeQueenPhaseName mirrors normalizing legacy aliases onto the two
// current phases; an unknown value passes through for the caller to reject.
func normalizeQueenPhaseName(value string) string {
	if alias, ok := legacyPhaseAliases[value]; ok {
		return alias
	}
	return value
}

// ValidQueenPhase reports whether phase names one of the two operating phases.
func ValidQueenPhase(phase string) bool {
	return phase == QueenPhaseIndependent || phase == QueenPhaseColony
}

// QueenPhaseState is the live phase of one queen session. It satisfies
// SessionPhaseState, so the session payload and the queen routes read it
// through the existing interface instead of a concrete type.
type QueenPhaseState struct {
	mu           sync.RWMutex
	phase        string
	queenID      string
	queenProfile map[string]any
	metaPath     string
	bus          *eventbus.EventBus
	// toolSurface is the session's live tool tier (PhaseToolSurface): the
	// get_current_tools / searchable / allowlist views upstream's phase state
	// owns directly. Attached when the session registers its chat runtime;
	// every reader degrades to empty when it was never attached.
	toolSurface *PhaseToolSurface
}

// NewQueenPhaseState builds a phase state for an already-resolved phase.
// metaPath is the session's meta.json ("" disables persistence, as upstream's
// meta_path=None). phase is normalized and invalid values fall back to
// independent so a state is never constructed in an unknown phase.
func NewQueenPhaseState(phase, queenID string, profile map[string]any, metaPath string, bus *eventbus.EventBus) *QueenPhaseState {
	phase = normalizeQueenPhaseName(strings.TrimSpace(phase))
	if !ValidQueenPhase(phase) {
		phase = QueenPhaseIndependent
	}
	return &QueenPhaseState{
		phase:        phase,
		queenID:      strings.TrimSpace(queenID),
		queenProfile: cloneQueenProfile(profile),
		metaPath:     strings.TrimSpace(metaPath),
		bus:          bus,
	}
}

func cloneQueenProfile(profile map[string]any) map[string]any {
	if profile == nil {
		return nil
	}
	out := make(map[string]any, len(profile))
	for key, value := range profile {
		out[key] = value
	}
	return out
}

// Phase returns the current phase (SessionPhaseState).
func (p *QueenPhaseState) Phase() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.phase
}

// QueenID returns the queen identity this phase runs under, or "" (the
// upstream getattr default when the identity hook has not stamped one).
func (p *QueenPhaseState) QueenID() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.queenID
}

// QueenProfile returns a copy of the queen profile, or nil.
func (p *QueenPhaseState) QueenProfile() map[string]any {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return cloneQueenProfile(p.queenProfile)
}

// MetaPath is the session meta.json this state persists into ("" when the
// session has no on-disk root).
func (p *QueenPhaseState) MetaPath() string {
	if p == nil {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.metaPath
}

// SetToolSurface attaches the session's live tool tier. Re-attaching replaces
// the previous surface (the chat runtime is the single source of tier state).
func (p *QueenPhaseState) SetToolSurface(surface *PhaseToolSurface) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.toolSurface = surface
	p.mu.Unlock()
}

// phaseSurface returns the attached tool surface, or nil.
func (p *QueenPhaseState) phaseSurface() *PhaseToolSurface {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.toolSurface
}

// The tool-tier getters below mirror the upstream QueenPhaseState methods the
// debug routes duck-type. A session with no attached runtime surfaces an empty
// view rather than hiding the method, matching Python (the methods always
// exist; the tier is just empty).

// GetCurrentTools is the eager (callable-now) tool set of the phase.
func (p *QueenPhaseState) GetCurrentTools() []tools.Tool {
	return p.phaseSurface().GetCurrentTools()
}

// GetSearchableTools is the allowlist-filtered searchable tier.
func (p *QueenPhaseState) GetSearchableTools() []tools.Tool {
	return p.phaseSurface().GetSearchableTools()
}

// UnregisteredAllowlistedNames are allowlisted names with no live MCP server.
func (p *QueenPhaseState) UnregisteredAllowlistedNames() map[string]struct{} {
	return p.phaseSurface().UnregisteredAllowlistedNames()
}

// MCPToolNamesAll is the MCP-origin name universe of the phase.
func (p *QueenPhaseState) MCPToolNamesAll() map[string]struct{} {
	return p.phaseSurface().MCPToolNamesAll()
}

// EnabledMCPTools exposes the per-queen allowlist (isNone = allow every MCP
// tool), upstream's enabled_mcp_tools None semantics.
func (p *QueenPhaseState) EnabledMCPTools() ([]string, bool) {
	return p.phaseSurface().EnabledMCPTools()
}

// IndependentTools is the phase's membership list before the allowlist gate.
func (p *QueenPhaseState) IndependentTools() []tools.Tool {
	return p.phaseSurface().IndependentTools()
}

// FilteredIndependentTools is IndependentTools after the allowlist gate.
func (p *QueenPhaseState) FilteredIndependentTools() []tools.Tool {
	return p.phaseSurface().FilteredIndependentTools()
}

// SetQueenIdentity stamps the identity the phase runs under, as the upstream
// identity hook does once per session start.
func (p *QueenPhaseState) SetQueenIdentity(queenID string, profile map[string]any) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.queenID = strings.TrimSpace(queenID)
	p.queenProfile = cloneQueenProfile(profile)
	p.mu.Unlock()
}

// SetPhase moves the session to phase, persists it, and announces the change
// on the session bus. It mirrors switch_to_independent / switch_to_colony's
// state+persist+emit sequence; a no-op switch returns without an event.
func (p *QueenPhaseState) SetPhase(phase string) error {
	if p == nil {
		return nil
	}
	phase = normalizeQueenPhaseName(strings.TrimSpace(phase))
	if !ValidQueenPhase(phase) {
		return fmt.Errorf("server: invalid queen phase %q", phase)
	}
	p.mu.Lock()
	previous := p.phase
	p.phase = phase
	bus := p.bus
	p.mu.Unlock()
	if previous == phase {
		return p.PersistPhase()
	}
	if err := p.PersistPhase(); err != nil {
		return err
	}
	if bus != nil {
		event := eventbus.NewAgentEvent(eventbus.EventQueenPhaseChanged, chatStreamID)
		event.Data = map[string]any{"phase": phase}
		bus.Publish(event)
	}
	return nil
}

// PersistPhase merges {"phase": current} into the session meta.json. A missing
// root or an unreadable file is not fatal: the live state stays authoritative
// and the next write heals the file.
func (p *QueenPhaseState) PersistPhase() error {
	if p == nil || p.metaPath == "" {
		return nil
	}
	return mergeSessionMeta(p.metaPath, map[string]any{sessionPhaseMetaKey: p.Phase()})
}

// MergeSessionMeta merges updates into a session's meta.json, keeping every
// other key (upstream QueenPhaseState._persist_meta / _start_queen's meta
// write). Exported for the session loader, which stamps the resolved queen
// identity next to the phase.
func MergeSessionMeta(metaPath string, updates map[string]any) error {
	return mergeSessionMeta(metaPath, updates)
}

// mergeSessionMeta merges updates into a session's meta.json, keeping every
// other key. The write is skipped when the file already carries the values,
// matching QueenPhaseState._persist_meta.
func mergeSessionMeta(metaPath string, updates map[string]any) error {
	if strings.TrimSpace(metaPath) == "" || len(updates) == 0 {
		return nil
	}
	existing := map[string]any{}
	if data, err := os.ReadFile(metaPath); err == nil {
		decoded := map[string]any{}
		if json.Unmarshal(data, &decoded) == nil {
			existing = decoded
		}
	}
	changed := false
	for key, value := range updates {
		if current, present := existing[key]; present && reflect.DeepEqual(current, value) {
			continue
		}
		existing[key] = value
		changed = true
	}
	if !changed {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(metaPath), 0o755); err != nil {
		return err
	}
	return atomicfs.WriteJSON(metaPath, existing, 0o644)
}

// ReadSessionPhase returns meta.json["phase"] under sessionDir, normalized and
// validated, or "" when the file or the key is absent.
func ReadSessionPhase(sessionDir string) string {
	dir := strings.TrimSpace(sessionDir)
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		return ""
	}
	meta := map[string]any{}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	raw, _ := meta[sessionPhaseMetaKey].(string)
	phase := normalizeQueenPhaseName(strings.TrimSpace(raw))
	if !ValidQueenPhase(phase) {
		return ""
	}
	return phase
}

// ResolveQueenPhase is the boot cascade from prepare_queen_session: an explicit
// initial_phase wins, then the persisted meta.json answer, then the physical
// bindings, then independent.
func ResolveQueenPhase(initial *string, persisted string, colonyBound bool) string {
	if initial != nil {
		if phase := normalizeQueenPhaseName(strings.TrimSpace(*initial)); ValidQueenPhase(phase) {
			return phase
		}
	}
	if phase := normalizeQueenPhaseName(strings.TrimSpace(persisted)); ValidQueenPhase(phase) {
		return phase
	}
	if colonyBound {
		return QueenPhaseColony
	}
	return QueenPhaseIndependent
}

// AttachSessionPhaseState resolves a session's phase, stamps it into
// meta.json and attaches the state to the session. It is the create/resume
// entry point: sessionDir is the session's on-disk root.
func AttachSessionPhaseState(session *Session, initial *string, sessionDir string) *QueenPhaseState {
	if session == nil {
		return nil
	}
	root := strings.TrimSpace(sessionDir)
	metaPath := ""
	if root != "" {
		metaPath = filepath.Join(root, "meta.json")
	}
	colonyBound := session.ColonyID != "" || session.WorkerPath != "" || session.Colony != nil
	phase := ResolveQueenPhase(initial, ReadSessionPhase(root), colonyBound)
	state := NewQueenPhaseState(phase, sessionQueenPhaseID(session), nil, metaPath, session.EventBus)
	if err := state.PersistPhase(); err != nil {
		// Persistence is best effort upstream (_persist_meta swallows OSError):
		// a read-only session tree must not fail the create.
		log.Printf("session %s: phase persistence unavailable: %v", session.ID, err)
	}
	session.PhaseState = state
	return state
}

// ApplyQueenPhase moves an existing session to phase and persists it. False
// means the session has no phase state to move (a cold record or a test stub).
func ApplyQueenPhase(session *Session, phase string) bool {
	if session == nil {
		return false
	}
	state, ok := session.PhaseState.(*QueenPhaseState)
	if !ok || state == nil {
		return false
	}
	if err := state.SetPhase(phase); err != nil {
		log.Printf("session %s: switching queen phase to %q failed: %v", session.ID, phase, err)
		return false
	}
	return true
}

// sessionQueenPhaseID is the queen identity stamped on the phase state. A
// session created under a named queen carries that id; the shared default
// queen keeps "" (the upstream getattr default when the identity hook has not
// run).
func sessionQueenPhaseID(session *Session) string {
	name := strings.TrimSpace(session.QueenName)
	if name == "default" {
		return ""
	}
	return name
}
