package memory

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Parity-exempt: the upstream reflection never reports its pass to a soul
// writer; the OnReflect seam is Go-only.
func TestReflectionSchedulerPublishesSuccessfulPasses(t *testing.T) {
	var published []LoopResult
	scheduler := &ReflectionScheduler{
		Config: func() ReflectionConfig {
			return ReflectionConfig{EveryTurns: 2, LongEvery: 2}
		},
		Spawn: func(work func()) { work() },
		Short: func(context.Context) LoopResult {
			return LoopResult{Success: true, ChangedFiles: []string{"queen:turn-learnings.md"}}
		},
		Long:      func(context.Context) LoopResult { return LoopResult{Success: false} },
		OnReflect: func(result LoopResult) { published = append(published, result) },
	}

	ctx := context.Background()
	scheduler.OnTurnDone(ctx, "queen", "turn_done")
	scheduler.OnTurnDone(ctx, "queen", "turn_done")
	if len(published) != 1 {
		t.Fatalf("published = %d, want only the successful short pass", len(published))
	}
	if len(published[0].ChangedFiles) != 1 || published[0].ChangedFiles[0] != "queen:turn-learnings.md" {
		t.Fatalf("published result = %#v", published[0])
	}

	// The teardown pass is a reflection like any other and publishes too.
	scheduler.Close(time.Second)
	if len(published) != 2 {
		t.Fatalf("published = %d after Close, want 2", len(published))
	}

	// A worker stream never reflects, so it never publishes.
	scheduler.OnTurnDone(ctx, "worker", "turn_done")
	if len(published) != 2 {
		t.Fatalf("worker stream published a pass: %d", len(published))
	}
}

// Parity-exempt: reading a reflection's changed memory files back as
// conclusions is the Go port's soul-sediment seam; upstream has no equivalent.
func TestConclusionsFromReflectionReadChangedMemoryFiles(t *testing.T) {
	queenDir := t.TempDir()
	writeFile(t, filepath.Join(queenDir, "turn-learnings.md"),
		"---\nname: turn-learnings\ntype: preference\ndescription: How this queen should answer\n---\nPrefer implementation-first answers.")
	writeFile(t, filepath.Join(queenDir, "no-description.md"),
		"---\nname: no-description\ntype: profile\n---\n# Notes\nPrefer terse answers.")
	dirs := []ScopeDir{{Scope: "queen", Dir: queenDir}}

	result := LoopResult{Success: true, ChangedFiles: []string{
		"queen:turn-learnings.md",
		"queen:no-description.md",
		"queen:deleted.md",
		"global:somewhere-else.md",
	}}
	conclusions := ConclusionsFromReflection(result, dirs)
	if len(conclusions) != 2 {
		t.Fatalf("conclusions = %#v", conclusions)
	}
	if conclusions[0].Text != "How this queen should answer" || conclusions[0].Filename != "turn-learnings.md" {
		t.Fatalf("first conclusion = %#v", conclusions[0])
	}
	if conclusions[1].Text != "Prefer terse answers." {
		t.Fatalf("fallback conclusion = %#v", conclusions[1])
	}

	if got := ConclusionsFromReflection(LoopResult{Success: false, ChangedFiles: result.ChangedFiles}, dirs); len(got) != 0 {
		t.Fatalf("a failed pass produced conclusions: %#v", got)
	}
	if got := ConclusionsFromReflection(LoopResult{Success: true}, dirs); len(got) != 0 {
		t.Fatalf("a pass that changed nothing produced conclusions: %#v", got)
	}

	// More changed files than the cap: the first ones win, deterministically.
	many := []string{}
	for i := 0; i < 5; i++ {
		name := "even-" + itoa(i) + ".md"
		writeFile(t, filepath.Join(queenDir, name), "---\ndescription: conclusion "+itoa(i)+"\n---\n")
		many = append(many, "queen:"+name)
	}
	capped := ConclusionsFromReflection(LoopResult{Success: true, ChangedFiles: many}, dirs)
	if len(capped) != MaxReflectionConclusions {
		t.Fatalf("capped conclusions = %d, want %d", len(capped), MaxReflectionConclusions)
	}
}
