package server

// Parity: framework/server/routes_queens.py::handle_queen_session
// Parity: framework/server/routes_queens.py::handle_select_queen_session
// Parity: framework/server/routes_queens.py::handle_new_queen_session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/eventbus"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/queen"
	"github.com/shark2202/go_hive/go-hive/internal/soul"
	"github.com/shark2202/go_hive/go-hive/internal/storage"
)

// queenLiveDM reports whether a live session is a DM of this queen. Upstream
// additionally excludes channel sessions (a QQ group's chat is the channel's,
// not the user's DM) and sessions retired by a fork (`superseded_by`): a
// forked-away session must not be served as live or parked by /session/new.
//
// Parity: framework/server/routes_queens.py::handle_queen_session
func queenLiveDM(session *Session, queenID string) bool {
	return session != nil &&
		session.QueenName == queenID &&
		session.ColonyID == "" &&
		session.WorkerPath == "" &&
		session.Colony == nil &&
		session.Channel() == "" &&
		session.SupersededBy == ""
}

// refreshQueenLiveIdentity re-materializes the identity of every live session
// of queenID after a profile or persona edit. Upstream's
// _refresh_live_queen_identity sets the phase state's queen profile (the next
// AgentLoop turn reads it) and re-publishes QUEEN_IDENTITY_SELECTED so the
// conversation header updates; this port also rebases the runtime's baked
// system prompt so the change reaches the next model request.
//
// Parity: framework/server/routes_queens.py::_refresh_live_queen_identity
func refreshQueenLiveIdentity(manager *SessionManager, queenID string, profile map[string]any) {
	if manager == nil {
		return
	}
	name, _ := profile["name"].(string)
	title, _ := profile["title"].(string)
	for _, session := range manager.ListSessions() {
		if session == nil || session.QueenName != queenID {
			continue
		}
		// Upstream skips a session without phase_state: only a materialized
		// queen carries an identity to refresh.
		phase, ok := session.PhaseState.(SessionPhaseState)
		if !ok || phase == nil {
			continue
		}
		if setter, ok := session.PhaseState.(interface {
			SetQueenIdentity(string, map[string]any)
		}); ok {
			setter.SetQueenIdentity(queenID, profile)
		}
		if session.Chat != nil {
			session.Chat.RefreshQueenIdentity(profile)
		}
		if session.EventBus != nil {
			event := eventbus.NewAgentEvent(eventbus.EventQueenIdentitySelected, chatStreamID)
			event.Data = map[string]any{
				"queen_id": queenID,
				"name":     name,
				"title":    title,
			}
			session.EventBus.Publish(event)
		}
	}
}

// chatQueenIdentityFromProfile renders a loaded profile's persona as the live
// identity prompt (max_examples=0, like materialize_queen_identity) and appends
// her soul, the same composition chatQueenIdentityPrompt builds at session
// creation. A nil profile keeps the generic chat identity.
func chatQueenIdentityFromProfile(homeRoot, queenName string, profile map[string]any) string {
	identity := chatIdentityPrompt
	if profile != nil {
		maxExamples := 0
		if prompt := strings.TrimSpace(queen.FormatQueenIdentityPrompt(profile, &maxExamples)); prompt != "" {
			identity = prompt
		}
	}
	if homeRoot != "" {
		home := hivehome.Home{Root: homeRoot}
		if soulPrompt := soul.PromptFor(HiveConfig(home), home.QueensDir(), queenName); soulPrompt != "" {
			identity = identity + "\n\n" + soulPrompt
		}
	}
	return identity
}

// chatQueenStaticPromptWithIdentity assembles the same cache-stable prompt
// chatQueenStaticPrompt builds, with the identity segment supplied by the
// caller: identity + the phase base prompt + the ambient credentials block.
func chatQueenStaticPromptWithIdentity(deps ChatRuntimeDeps, identity string) string {
	return queen.StaticPrompt(queen.StaticPromptOptions{
		Identity:    identity,
		Base:        queen.PhasePrompt(chatPhaseLabel(deps)),
		Credentials: chatCredentialsAmbientBlock(deps),
		MCPCLI:      chatMCPCLIPrompt(deps),
	})
}

// RefreshQueenIdentity rebases this runtime's cache-stable system prompt on a
// just-saved profile, so the next model request carries the new persona. The
// turn goroutine is the conversation's only writer, so an edit landing while a
// turn runs waits for the next idle edit; the phase state and the identity
// event already carry the change immediately.
func (r *ChatRuntime) RefreshQueenIdentity(profile map[string]any) {
	if r == nil || r.conv == nil || r.agentCtx == nil {
		return
	}
	if r.turnActive.Load() {
		return
	}
	identity := chatQueenIdentityFromProfile(r.deps.HomeRoot, r.queenName, profile)
	r.agentCtx.IdentityPrompt = identity
	r.conv.UpdateSystemPrompt(chatQueenStaticPromptWithIdentity(r.deps, identity), nil)
}

func queenReadMeta(sessionDir string) map[string]any {
	data, err := os.ReadFile(filepath.Join(sessionDir, "meta.json"))
	if err != nil {
		return map[string]any{}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return map[string]any{}
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return object
}

func queenSessionMeta(deps QueenRouteDeps, sessionID string) map[string]any {
	dir, err := FindQueenSessionDir(deps.Home, sessionID)
	if err != nil {
		return map[string]any{}
	}
	return queenReadMeta(dir)
}

// queenCreateBoundSession mirrors routes_queens._create_bound_queen_session.
func queenCreateBoundSession(deps QueenRouteDeps, queenID string, initialPrompt, initialPhase any, resumeFrom string) (*Session, error) {
	var colonyID *string
	if resumeFrom != "" {
		meta := queenSessionMeta(deps, resumeFrom)
		candidate := queenStr(meta["colony_id"])
		if candidate == "" {
			if legacy := queenStr(meta["agent_path"]); legacy != "" {
				candidate = filepath.Base(filepath.Clean(legacy))
			}
		}
		if candidate != "" {
			colonyID = &candidate
		}
	}
	if deps.CreateSession == nil {
		return nil, errors.New("create_session is not wired")
	}
	return deps.CreateSession(QueenSessionCreateParams{
		ColonyID:        colonyID,
		QueenResumeFrom: &resumeFrom,
		InitialPrompt:   initialPrompt,
		QueenName:       &queenID,
		InitialPhase:    initialPhase,
	})
}

// Parity: framework/server/routes_queens.py::handle_queen_session
func handleQueenSession(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	queen.EnsureDefaultQueens()
	if _, err := queenLoadProfile(deps, queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	body, ok := queenOptionalJSONBody(w, r)
	if !ok {
		return
	}
	initialPrompt := body["initial_prompt"]
	initialPhase := body["initial_phase"]

	if deps.Manager != nil {
		var latest *Session
		for _, session := range deps.Manager.ListSessions() {
			if !queenLiveDM(session, queenID) {
				continue
			}
			if latest == nil || session.LoadedAt > latest.LoadedAt {
				latest = session
			}
		}
		if latest != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"session_id": latest.ID,
				"queen_id":   queenID,
				"status":     "live",
			})
			return
		}
	}

	resumeFrom := ""
	sessionsDir := filepath.Join(deps.queensDir(), queenID, "sessions")
	if entries, err := os.ReadDir(sessionsDir); err == nil {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, name := range names {
			meta := queenReadMeta(filepath.Join(sessionsDir, name))
			if pyTruthy(meta["colony_fork"]) || pyTruthy(meta["superseded_by"]) || pyTruthy(meta["colony_spawned"]) || pyTruthy(meta["channel"]) {
				continue
			}
			resumeFrom = name
			break
		}
	}

	if resumeFrom != "" {
		session, err := queenCreateBoundSession(deps, queenID, initialPrompt, initialPhase, resumeFrom)
		if err != nil || session == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id": session.ID,
			"queen_id":   queenID,
			"status":     "resumed",
		})
		return
	}

	if deps.CreateSession == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	session, err := deps.CreateSession(QueenSessionCreateParams{
		InitialPrompt: initialPrompt,
		QueenName:     &queenID,
		InitialPhase:  initialPhase,
	})
	if err != nil || session == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": session.ID,
		"queen_id":   queenID,
		"status":     "created",
	})
}

// Parity: framework/server/routes_queens.py::_session_belongs_to_queen
func queenSessionBelongsTo(deps QueenRouteDeps, sessionID, queenID string) bool {
	if deps.Manager != nil {
		if live := deps.Manager.GetSession(sessionID); live != nil {
			return live.QueenName == queenID
		}
	}
	dir, err := FindQueenSessionDir(deps.Home, sessionID)
	if err != nil || !entryIsDir(dir) {
		return false
	}
	cleaned := filepath.Clean(dir)
	return filepath.Base(filepath.Dir(cleaned)) == "sessions" &&
		filepath.Base(filepath.Dir(filepath.Dir(cleaned))) == queenID
}

// Parity: framework/server/routes_queens.py::handle_select_queen_session
func handleQueenSelectSession(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	queen.EnsureDefaultQueens()
	if _, err := queenLoadProfile(deps, queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	body, ok := queenOptionalJSONBody(w, r)
	if !ok {
		return
	}
	target, _ := body["session_id"].(string)
	if strings.TrimSpace(target) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "session_id is required"})
		return
	}
	target = strings.TrimSpace(target)
	if !queenSessionBelongsTo(deps, target, queenID) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": fmt.Sprintf("Session '%s' does not belong to queen '%s'", target, queenID),
		})
		return
	}

	seen := map[string]bool{target: true}
	chainMeta := queenSessionMeta(deps, target)
	for {
		successor, isString := chainMeta["superseded_by"].(string)
		if !isString {
			break
		}
		if seen[successor] {
			break
		}
		seen[successor] = true
		target = successor
		chainMeta = queenSessionMeta(deps, successor)
	}

	if deps.Manager != nil {
		if live := deps.Manager.GetSession(target); live != nil {
			if live.ColonyID != "" {
				writeJSON(w, http.StatusOK, map[string]any{
					"session_id": live.ID,
					"queen_id":   queenID,
					"status":     "colony",
					"colony_id":  live.ColonyID,
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"session_id": live.ID,
				"queen_id":   queenID,
				"status":     "live",
			})
			return
		}
	}

	if colonyID, found := DeriveResumeColonyID(deps.Home, target); found && colonyID != "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"session_id": target,
			"queen_id":   queenID,
			"status":     "colony",
			"colony_id":  colonyID,
		})
		return
	}

	meta := queenSessionMeta(deps, target)
	initialPhase := "independent"
	if agentPath := queenStr(meta["agent_path"]); agentPath != "" {
		initialPhase = "colony"
	}
	session, err := queenCreateBoundSession(deps, queenID, nil, initialPhase, target)
	if err != nil || session == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": session.ID,
		"queen_id":   queenID,
		"status":     "resumed",
	})
}

// Parity: framework/server/routes_queens.py::handle_new_queen_session
func handleQueenNewSession(w http.ResponseWriter, r *http.Request, deps QueenRouteDeps) {
	queenID := r.PathValue("queen_id")
	queen.EnsureDefaultQueens()
	if _, err := queenLoadProfile(deps, queenID); err != nil {
		queenProfileError(w, err)
		return
	}
	body := map[string]any{}
	if queenCanReadBody(r) {
		decoded, err := decodeJSONValue(r)
		if err != nil {
			writeJSONBodyFailure(w, err, http.StatusBadRequest, "Invalid JSON body")
			return
		}
		object, ok := decoded.(map[string]any)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Request body must be a JSON object"})
			return
		}
		body = object
	}

	initialPrompt := body["initial_prompt"]
	phaseValue := normalizeLegacyPhaseValue(body["initial_phase"])
	if !pyTruthy(phaseValue) {
		phaseValue = "independent"
	}
	phaseText, isString := phaseValue.(string)
	if !isString || (phaseText != "colony" && phaseText != "independent") {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": fmt.Sprintf("Invalid initial_phase '%s'", pyStr(phaseValue)),
			"valid": []string{"colony", "independent"},
		})
		return
	}

	parkOthers := true
	if value, ok := body["park_others"]; ok {
		parkOthers = pyTruthy(value)
	}
	if parkOthers && deps.Manager != nil {
		for _, prior := range deps.Manager.ListSessions() {
			if !queenLiveDM(prior, queenID) {
				continue
			}
			if deps.StopSession != nil {
				_ = deps.StopSession(prior.ID)
			} else {
				deps.Manager.StopSession(prior.ID)
			}
			queenCleanupEmptySession(deps, prior.ID)
		}
	}

	if deps.CreateSession == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	session, err := deps.CreateSession(QueenSessionCreateParams{
		InitialPrompt: initialPrompt,
		QueenName:     &queenID,
		InitialPhase:  phaseText,
		CRMSetup:      pyTruthy(body["crm_setup"]),
	})
	if err != nil || session == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": session.ID,
		"queen_id":   queenID,
		"status":     "created",
	})
}

// queenCleanupEmptySession removes a parked session's directory when it never
// recorded a message, so double-clicking New Session doesn't litter history.
func queenCleanupEmptySession(deps QueenRouteDeps, sessionID string) {
	dir, err := FindQueenSessionDir(deps.Home, sessionID)
	if err != nil || !entryIsDir(dir) {
		return
	}
	summary := storage.ReadSummary(dir)
	count := 0
	if summary != nil {
		count = summary.MessageCount
	}
	if count != 0 {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "conversations", "parts")); err == nil {
		return
	}
	_ = os.RemoveAll(dir)
}

// normalizeLegacyPhaseValue mirrors queen_lifecycle_tools.normalize_legacy_phase:
// aliases map to current phases, anything else passes through untouched.
func normalizeLegacyPhaseValue(value any) any {
	text, ok := value.(string)
	if !ok {
		return value
	}
	if alias, ok := legacyPhaseAliases[text]; ok {
		return alias
	}
	return text
}

func queenCanReadBody(r *http.Request) bool {
	if r.Body == nil {
		return false
	}
	if r.ContentLength > 0 {
		return true
	}
	for _, encoding := range r.TransferEncoding {
		if strings.EqualFold(encoding, "chunked") {
			return true
		}
	}
	return false
}

// queenOptionalJSONBody mirrors `await request.json() if request.can_read_body
// else {}`: an unreadable body surfaces as the generic 500 envelope.
func queenOptionalJSONBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	if !queenCanReadBody(r) {
		return map[string]any{}, true
	}
	decoded, err := decodeJSONValue(r)
	if err != nil {
		writeJSONBodyFailure(w, err, http.StatusInternalServerError, internalServerError)
		return nil, false
	}
	object, ok := decoded.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": internalServerError})
		return nil, false
	}
	return object, true
}

// queenDecodeObject decodes a JSON object body, writing the Python handler's
// 400 message for unparseable JSON or a non-object value.
