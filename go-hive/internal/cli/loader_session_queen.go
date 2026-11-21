package cli

// Cold-start queen identity: which queen a session created without an explicit
// queen_name belongs to.
//
// Upstream's SessionManager.create_session always starts the queen through
// _ensure_session_queen_identity, so a brand-new DM session is bound to a
// queen before the create response is written:
//
//  1. an explicit queen_name from the body (already validated by the route);
//  2. the queen the resumed session was persisted under (meta.json["queen_id"],
//     else the queen directory holding it);
//  3. select_queen(initial_prompt, session.llm): the static default queen for
//     an empty message, the classifier for a message-bearing create, and the
//     static default again on any classifier failure.
//
// Only step 3 lives here: steps 1 and 2 are resolved while planning the
// session (planChatSession), because they decide the transcript tree. Without
// this step a cold Go session stayed unbound (queen_id/queen_name null,
// queens/default tree) while Python bound queens/queen_growth — the strict
// endpoint probe's POST /api/sessions, GET /api/sessions and
// GET /api/sessions/{id} diffs.
//
// Parity: framework/server/session_manager.py::_ensure_session_queen_identity
// Parity: framework/agents/queen/queen_profiles.py::select_queen

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
	"github.com/aden-hive/hive-go/internal/llm"
	hiveserver "github.com/aden-hive/hive-go/internal/server"
)

// resolveCreateIdentity picks the queen a create binds and the model its
// provider will use. The model comes first: a create that carries an
// initial_prompt asks the queen classifier on it, as upstream's
// _ensure_session_queen_identity does through session.llm.
//
// plan already resolved an explicit queen_name and the queen a resumed session
// was persisted under; only a still-unbound fresh create reaches the selector
// (empty message → static default, message-bearing → classifier).
func (r *Runner) resolveCreateIdentity(
	params hiveserver.CreateSessionParams,
	plan chatSessionPlan,
	home hivehome.Home,
	defaultModel string,
) (string, string) {
	requestedModel := ""
	if params.Model != nil {
		requestedModel = *params.Model
	}
	if strings.TrimSpace(requestedModel) == "" {
		// Fall back to the serve --model flag before the configured default.
		requestedModel = defaultModel
	}
	queenScope := plan.QueenScope
	if queenScope == "default" && plan.ResumedDir == "" {
		if resolved := r.selectColdStartQueen(home, params.InitialPrompt, requestedModel); resolved != "" {
			queenScope = resolved
		}
	}
	return queenScope, requestedModel
}

// selectColdStartQueen returns the queen a fresh create with no explicit or
// resumed identity binds to. requestedModel is the session's resolved model:
// the classifier runs on the same base provider the session itself would get.
//
// Every failure path returns the static default, exactly as
// select_queen_with_reason does (it never propagates a classifier error).
func (r *Runner) selectColdStartQueen(home hivehome.Home, initialPrompt *string, requestedModel string) string {
	prompt := ""
	if initialPrompt != nil {
		prompt = strings.TrimSpace(*initialPrompt)
	}
	if prompt == "" {
		return hiveserver.DefaultQueenID()
	}
	provider, err := chatProviderFactory(home, requestedModel)
	if err != nil || provider == nil {
		return hiveserver.DefaultQueenID()
	}
	selection := hiveserver.SelectQueenWithReason(context.Background(), prompt, provider, llm.AuxMaxTokens())
	if strings.TrimSpace(selection.QueenID) == "" {
		return hiveserver.DefaultQueenID()
	}
	return strings.TrimSpace(selection.QueenID)
}

// stampSessionQueenMeta records the bound queen (and the create time on a new
// session root) in meta.json before the loop starts, as _start_queen does.
// Persistence is best effort: a read-only session tree must not fail the
// create, and the live phase state stays authoritative.
func (r *Runner) stampSessionQueenMeta(sessionDir, queenID string) {
	queenID = strings.TrimSpace(queenID)
	if sessionDir == "" || queenID == "" || queenID == "default" {
		return
	}
	metaPath := filepath.Join(sessionDir, "meta.json")
	updates := map[string]any{"queen_id": queenID}
	if meta, ok := readJSONObject(metaPath); !ok || meta["created_at"] == nil {
		updates["created_at"] = float64(time.Now().UnixNano()) / 1e9
	}
	if err := hiveserver.MergeSessionMeta(metaPath, updates); err != nil {
		fmt.Fprintf(r.Stdout, "Warning: session meta unavailable for %s: %v\n", sessionDir, err)
	}
}
