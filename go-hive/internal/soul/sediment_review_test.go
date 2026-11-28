package soul

// The review half of the Go-only sediment seam: a soul in "review" mode must be
// changed only by an owner decision, so the sediment may stage a proposal but
// never write soul.json itself. Upstream has no sediment at all; there the same
// invariant is held by soul_reflection.reflect (mode == "review" only proposes)
// and by soul_reflection.due (a pending proposal blocks the next pass).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func reviewSedimentConfig(limit int) *SoulConfig {
	cfg := sedimentTestConfig(limit)
	cfg.Mode = "review"
	return cfg
}

func proposalFiles(t *testing.T, queensDir, queenID string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(SoulDir(queensDir, queenID), "proposals"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read proposals: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// Parity-exempt: Go-only sediment seam. The invariant it keeps — a review-mode
// soul changes only when the owner decides — is upstream's
// soul_reflection.reflect/handle_decide behaviour.
func TestSedimentReviewModeProposesAndDecideApplies(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	cfg := reviewSedimentConfig(300)
	now := sedimentTestNow(t)

	result, err := Sediment(SedimentOptions{
		QueensDir: queensDir,
		QueenID:   "queen_a",
		Config:    cfg,
		Conclusions: []SedimentConclusion{
			{Text: "prefers implementation-first answers", Source: "turn-learnings.md"},
			{Text: "keeps   her   drafts  short", Source: "style.md"},
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Sediment: %v", err)
	}
	if result.Written || !result.Proposed || result.Blocked {
		t.Fatalf("review result = %#v", result)
	}
	if result.ProposalID != "2026-11-19-memory-093000" {
		t.Fatalf("proposal id = %q", result.ProposalID)
	}
	if result.Version != 0 || result.Section != "now" || len(result.Added) != 2 {
		t.Fatalf("review result = %#v", result)
	}

	// The soul itself is untouched: no version, no history, no log, and no
	// sediment bookkeeping either (nothing of the sediment's own was written).
	if _, err := os.Stat(filepath.Join(SoulDir(queensDir, "queen_a"), SoulFile)); !os.IsNotExist(err) {
		t.Fatalf("review mode wrote soul.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(SoulDir(queensDir, "queen_a"), "log.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("review mode wrote log.jsonl: %v", err)
	}
	if _, err := os.Stat(filepath.Join(SoulDir(queensDir, "queen_a"), SedimentFile)); !os.IsNotExist(err) {
		t.Fatalf("review mode wrote sediment.json: %v", err)
	}

	proposal := LoadProposal(queensDir, "queen_a", result.ProposalID)
	if proposal == nil {
		t.Fatalf("proposal %q not stored", result.ProposalID)
	}
	if proposal.Status != "pending" || proposal.BaseVersion != 0 || len(proposal.Edits) != 2 {
		t.Fatalf("proposal = %#v", proposal)
	}
	if proposal.Summary != "2 from memory reflection" {
		t.Fatalf("summary = %q", proposal.Summary)
	}
	if len(proposal.Sources) != 2 || proposal.Sources[0] != "memory turn-learnings.md" || proposal.Sources[1] != "memory style.md" {
		t.Fatalf("sources = %#v", proposal.Sources)
	}
	for _, edit := range proposal.Edits {
		if edit.Op != "add" || edit.Section != "now" || edit.Date != "2026-11-19" {
			t.Fatalf("edit = %#v", edit)
		}
		if edit.Evidence == nil {
			t.Fatalf("edit evidence is null, want the proposal shape's []: %#v", edit)
		}
	}
	if pending := PendingProposal(queensDir, "queen_a"); pending == nil || pending.ID != result.ProposalID {
		t.Fatalf("pending proposal = %#v", pending)
	}

	// The ordinary soul decide route applies it, as the owner.
	if _, err := Decide(queensDir, cfg, "queen_a", proposal, nil, "owner"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	stored := readSoulFile(t, queensDir, "queen_a")
	if stored.Version != 1 || len(stored.Items("now")) != 2 {
		t.Fatalf("soul after decide = %#v", stored)
	}
	if got := soulItemTexts(stored, "now"); got[0] != "prefers implementation-first answers" || got[1] != "keeps her drafts short" {
		t.Fatalf("items after decide = %v", got)
	}
	logData, err := os.ReadFile(filepath.Join(SoulDir(queensDir, "queen_a"), "log.jsonl"))
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(logData), `"author": "owner"`) || !strings.Contains(string(logData), "2 of 2 changes") {
		t.Fatalf("log = %q", string(logData))
	}
	if applied := LoadProposal(queensDir, "queen_a", result.ProposalID); applied == nil || applied.Status != "applied" {
		t.Fatalf("proposal after decide = %#v", applied)
	}

	// With the earlier proposal decided, the next pass may stage another one —
	// and it is a distinct file, not an overwrite.
	second, err := Sediment(SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_a",
		Config:      cfg,
		Conclusions: []SedimentConclusion{{Text: "a new conclusion", Source: "new.md"}},
		Now:         now,
	})
	if err != nil {
		t.Fatalf("second Sediment: %v", err)
	}
	if !second.Proposed || second.ProposalID == result.ProposalID {
		t.Fatalf("second result = %#v", second)
	}
	if files := proposalFiles(t, queensDir, "queen_a"); len(files) != 2 {
		t.Fatalf("proposal files = %v, want two", files)
	}
}

// Parity-exempt: Go-only sediment seam; the "one pending proposal at a time"
// rule it mirrors is soul_reflection.due's review-mode guard.
func TestSedimentReviewModeBlockedByPendingProposal(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	cfg := reviewSedimentConfig(300)
	waiting := &Proposal{ID: "2026-11-18-080000", Status: "pending", Summary: "earlier reflection"}
	if err := SaveProposal(queensDir, "queen_a", waiting); err != nil {
		t.Fatalf("seed pending proposal: %v", err)
	}

	result, err := Sediment(SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_a",
		Config:      cfg,
		Conclusions: []SedimentConclusion{{Text: "waits for the decision"}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("Sediment: %v", err)
	}
	if !result.Blocked || result.Proposed || result.Written {
		t.Fatalf("blocked result = %#v", result)
	}
	if files := proposalFiles(t, queensDir, "queen_a"); len(files) != 1 {
		t.Fatalf("proposal files = %v, want only the earlier one", files)
	}
	if _, err := os.Stat(filepath.Join(SoulDir(queensDir, "queen_a"), SoulFile)); !os.IsNotExist(err) {
		t.Fatalf("blocked sediment wrote soul.json: %v", err)
	}

	// Deciding the earlier proposal (with no edits it is dismissed) reopens the
	// gate: the next pass stages its own proposal.
	if _, err := Decide(queensDir, cfg, "queen_a", waiting, nil, "owner"); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	reopened, err := Sediment(SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_a",
		Config:      cfg,
		Conclusions: []SedimentConclusion{{Text: "now it may be proposed"}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("Sediment after decide: %v", err)
	}
	if !reopened.Proposed || reopened.Blocked {
		t.Fatalf("reopened result = %#v", reopened)
	}
	if _, err := os.Stat(filepath.Join(SoulDir(queensDir, "queen_a"), SoulFile)); !os.IsNotExist(err) {
		t.Fatalf("reopened sediment wrote soul.json: %v", err)
	}
}

// Parity-exempt: Go-only sediment seam, two-sided regression. Auto mode keeps
// writing directly (upstream's mode == "auto" reflection applies at once) and
// review mode never touches the soul — a guard that always skipped or always
// wrote would fail one half of this test.
func TestSedimentModeBoundsTheWritePathTwoSided(t *testing.T) {
	// Auto: the soul is written at once, and no proposal exists.
	autoDir := sedimentTestQueens(t, "queen_a")
	autoResult, err := Sediment(SedimentOptions{
		QueensDir:   autoDir,
		QueenID:     "queen_a",
		Config:      sedimentTestConfig(300),
		Conclusions: []SedimentConclusion{{Text: "auto writes", Source: "a.md"}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("auto Sediment: %v", err)
	}
	if !autoResult.Written || autoResult.Proposed {
		t.Fatalf("auto result = %#v", autoResult)
	}
	if stored := readSoulFile(t, autoDir, "queen_a"); stored.Version != 1 || len(stored.Items("now")) != 1 {
		t.Fatalf("auto soul = %#v", stored)
	}
	if files := proposalFiles(t, autoDir, "queen_a"); len(files) != 0 {
		t.Fatalf("auto mode staged proposals: %v", files)
	}

	// Review: the same write stages a proposal and leaves the soul at version 0.
	reviewDir := sedimentTestQueens(t, "queen_a")
	reviewResult, err := Sediment(SedimentOptions{
		QueensDir:   reviewDir,
		QueenID:     "queen_a",
		Config:      reviewSedimentConfig(300),
		Conclusions: []SedimentConclusion{{Text: "auto writes", Source: "a.md"}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("review Sediment: %v", err)
	}
	if reviewResult.Written || !reviewResult.Proposed {
		t.Fatalf("review result = %#v", reviewResult)
	}
	if _, err := os.Stat(filepath.Join(SoulDir(reviewDir, "queen_a"), SoulFile)); !os.IsNotExist(err) {
		t.Fatalf("review mode wrote the soul the auto run wrote: %v", err)
	}
}

// Parity-exempt: Go-only sediment seam. In review mode nothing may evict: an
// item an earlier proposal introduced belongs to the owner by the time the new
// proposal is decided, so a full section drops the conclusion instead.
func TestSedimentReviewModeDropsInsteadOfEvicting(t *testing.T) {
	queensDir := sedimentTestQueens(t, "queen_a")
	cfg := reviewSedimentConfig(20)
	if _, err := Save(queensDir, cfg, "queen_a",
		map[string][]Item{"now": {{Text: "human note stays", Date: "2026-11-01"}}},
		"owner", "edited by hand", nil); err != nil {
		t.Fatalf("seed soul: %v", err)
	}

	result, err := Sediment(SedimentOptions{
		QueensDir:   queensDir,
		QueenID:     "queen_a",
		Config:      cfg,
		Conclusions: []SedimentConclusion{{Text: "does not fit at all"}},
		Now:         sedimentTestNow(t),
	})
	if err != nil {
		t.Fatalf("Sediment: %v", err)
	}
	if result.Proposed || result.Written || result.Dropped != 1 {
		t.Fatalf("full-section result = %#v", result)
	}
	if files := proposalFiles(t, queensDir, "queen_a"); len(files) != 0 {
		t.Fatalf("a conclusion that cannot fit was proposed: %v", files)
	}
	if stored := readSoulFile(t, queensDir, "queen_a"); stored.Version != 1 || len(stored.Items("now")) != 1 {
		t.Fatalf("full section was rewritten: %#v", stored)
	}
}
