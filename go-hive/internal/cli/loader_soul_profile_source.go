package cli

// The reflection's identity seam. internal/soul must not import
// internal/queen (the profile store sits in the orchestration layer above the
// capability layer), so the reflection reads a queen's profile through
// soul.QueenProfileSource and the assembly layer — here — injects the
// queen-backed implementation wherever soul.Reflect or the daily runner is
// assembled: loader_soul_reflect.go and loader_soul_daily.go.
//
// Parity: framework/agents/queen/soul_reflection.py::_identity

import (
	queenstore "github.com/shark2202/go_hive/go-hive/internal/queen"
)

// soulProfileSource is the queen-backed soul.QueenProfileSource. Both calls
// forward verbatim: the reflection sees exactly what it read before the seam
// existed.
type soulProfileSource struct{}

// LoadQueenProfile reads queens/<id>/profile.yaml, or materializes her
// built-in default when the file does not exist yet.
func (soulProfileSource) LoadQueenProfile(queensDir, queenID string) (map[string]any, error) {
	return queenstore.LoadQueenProfile(queensDir, queenID)
}

// FormatQueenIdentityPrompt renders the profile's identity block; the
// reflection passes max_examples = 0 (no worked examples).
func (soulProfileSource) FormatQueenIdentityPrompt(profile map[string]any, maxExamples *int) string {
	return queenstore.FormatQueenIdentityPrompt(profile, maxExamples)
}
