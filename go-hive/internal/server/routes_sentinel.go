package server

// Parity: framework/server/routes_sentinel.py::register_routes

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
	"github.com/shark2202/go_hive/go-hive/internal/sentinel"
)

const (
	sentinelTelegramAPI = "https://api.telegram.org"
	sentinelSlackAPI    = "https://slack.com/api"
)

var sentinelValidChannels = map[string]bool{
	"hive":     true,
	"telegram": true,
	"slack":    true,
}

// SentinelNotificationsConfig mirrors framework/sentinel/store.py::NotificationsConfig.
// The sentinel engine's store resolves per-colony defaults (channel "hive",
// sentinel_enabled true) before handing the value to these routes.
type SentinelNotificationsConfig struct {
	SentinelEnabled      bool
	Channel              string
	Target               map[string]any
	Allowlist            []string
	Thread               map[string]any
	ClassifyAfterSeconds *float64
}

// SentinelNotificationsUpdate mirrors the keyword arguments of
// framework/sentinel/store.py::update_notifications_config. Channel is nil
// when the client sent none, so a store can persist JSON null and resolve the
// "hive" default on load, exactly like the Python store.
type SentinelNotificationsUpdate struct {
	SentinelEnabled      bool
	Channel              *string
	Target               map[string]any
	Allowlist            []string
	ClassifyAfterSeconds *float64
}

// SentinelStore is the seam onto framework/sentinel/store.py, matching
// internal/sentinel.Store's shape: load resolves defaults and never fails
// (an unreadable file falls back to channel "hive" + enabled). Update must
// return an error matching os.ErrNotExist for a missing colony directory;
// the engine's sentinel.ErrColonyNotFound adapter maps to it.
type SentinelStore interface {
	LoadNotificationsConfig(colonyID string) SentinelNotificationsConfig
	UpdateNotificationsConfig(colonyID string, update SentinelNotificationsUpdate) error
}

// SentinelSendResult mirrors framework/sentinel/notifier.py::NotifierResult.
type SentinelSendResult struct {
	OK        bool
	MessageID string
	Error     string
}

// SentinelNotifier is the seam onto framework/sentinel/notifier.py.
type SentinelNotifier interface {
	TelegramToken() string
	SlackBotToken() string
	SlackAppToken() string
	Send(channel string, target map[string]any, text string, threadRef map[string]any) (SentinelSendResult, error)
}

// SentinelRouteDeps carries the sentinel engine handles plus the HTTP base
// URLs (overridden by tests to avoid the network).
type SentinelRouteDeps struct {
	Home             hivehome.Home
	Store            SentinelStore
	Notifier         SentinelNotifier
	HTTPClient       *http.Client
	TelegramAPIBase  string
	SlackAPIBase     string
	RefreshListeners func()
}

// refreshSentinelListeners is SentinelRouteDeps.RefreshListeners' production
// hook: a saved notification config may have just connected (or disconnected)
// a channel, so the process-global manager re-evaluates its inbound listeners
// now instead of on the next restart. An unstarted manager is a no-op.
//
// Parity: framework/server/routes_sentinel.py::handle_put_notifications
func refreshSentinelListeners() {
	if manager := sentinel.GetSentinelManager(); manager != nil {
		manager.RefreshListeners()
	}
}

func RegisterSentinelRoutes(mux *http.ServeMux, deps SentinelRouteDeps) {
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if deps.TelegramAPIBase == "" {
		deps.TelegramAPIBase = sentinelTelegramAPI
	}
	if deps.SlackAPIBase == "" {
		deps.SlackAPIBase = sentinelSlackAPI
	}
	deps.TelegramAPIBase = strings.TrimSuffix(deps.TelegramAPIBase, "/")
	deps.SlackAPIBase = strings.TrimSuffix(deps.SlackAPIBase, "/")

	mux.HandleFunc("GET /api/colony/{colony_id}/notifications", func(w http.ResponseWriter, r *http.Request) {
		handleGetNotifications(w, r, deps)
	})
	mux.HandleFunc("PUT /api/colony/{colony_id}/notifications", func(w http.ResponseWriter, r *http.Request) {
		handlePutNotifications(w, r, deps)
	})
	mux.HandleFunc("POST /api/colony/{colony_id}/notifications/test", func(w http.ResponseWriter, r *http.Request) {
		handleTestNotification(w, r, deps)
	})
	mux.HandleFunc("GET /api/sentinel/credentials", func(w http.ResponseWriter, r *http.Request) {
		handleSentinelCredentialStatus(w, r, deps)
	})
	mux.HandleFunc("POST /api/sentinel/telegram/validate", func(w http.ResponseWriter, r *http.Request) {
		handleTelegramValidate(w, r, deps)
	})
	mux.HandleFunc("POST /api/sentinel/telegram/detect-chat", func(w http.ResponseWriter, r *http.Request) {
		handleTelegramDetectChat(w, r, deps)
	})
	mux.HandleFunc("POST /api/sentinel/slack/validate", func(w http.ResponseWriter, r *http.Request) {
		handleSlackValidate(w, r, deps)
	})
}

// Parity: framework/server/routes_sentinel.py::_colony_missing
func sentinelColonyMissing(home hivehome.Home, colonyID string) bool {
	_, err := os.Stat(filepath.Join(home.ColoniesDir(), colonyID, "metadata.json"))
	return err != nil
}

// Parity: framework/server/routes_sentinel.py::_channel_status
func sentinelChannelStatus(notifier SentinelNotifier) map[string]any {
	telegram := ""
	slackBot := ""
	slackApp := ""
	if notifier != nil {
		telegram = notifier.TelegramToken()
		slackBot = notifier.SlackBotToken()
		slackApp = notifier.SlackAppToken()
	}
	return map[string]any{
		"telegram": map[string]any{"configured": telegram != ""},
		"slack": map[string]any{
			"bot": slackBot != "",
			"app": slackApp != "",
		},
	}
}

// Parity: framework/server/routes_sentinel.py::_config_payload
func sentinelConfigPayload(colonyID string, cfg SentinelNotificationsConfig, notifier SentinelNotifier) map[string]any {
	target := cfg.Target
	if target == nil {
		target = map[string]any{}
	}
	allowlist := cfg.Allowlist
	if allowlist == nil {
		allowlist = []string{}
	}
	thread := cfg.Thread
	if thread == nil {
		thread = map[string]any{}
	}
	var classifyAfter any
	if cfg.ClassifyAfterSeconds != nil {
		classifyAfter = *cfg.ClassifyAfterSeconds
	}
	return map[string]any{
		"colony_id":              colonyID,
		"sentinel_enabled":       cfg.SentinelEnabled,
		"channel":                cfg.Channel,
		"target":                 target,
		"allowlist":              allowlist,
		"thread":                 thread,
		"classify_after_seconds": classifyAfter,
		"credentials":            sentinelChannelStatus(notifier),
	}
}

func sentinelStoreUnavailable(w http.ResponseWriter) {
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "sentinel store not configured"})
}

func sentinelOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func handleGetNotifications(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if validateColonyName(colonyID) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}
	if sentinelColonyMissing(deps.Home, colonyID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Colony '%s' not found", colonyID)})
		return
	}
	if deps.Store == nil {
		sentinelStoreUnavailable(w)
		return
	}
	cfg := deps.Store.LoadNotificationsConfig(colonyID)
	writeJSON(w, http.StatusOK, sentinelConfigPayload(colonyID, cfg, deps.Notifier))
}

func handlePutNotifications(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if validateColonyName(colonyID) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}
	if sentinelColonyMissing(deps.Home, colonyID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Colony '%s' not found", colonyID)})
		return
	}
	if deps.Store == nil {
		sentinelStoreUnavailable(w)
		return
	}

	value, err := decodeJSONValue(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid JSON body"})
		return
	}
	body, ok := value.(map[string]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Body must be a JSON object"})
		return
	}

	channel := pyStrOrEmpty(body["channel"])
	enabled := pyTruthy(body["sentinel_enabled"])
	if enabled && !sentinelValidChannels[channel] {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel must be 'telegram' or 'slack' when enabled"})
		return
	}
	target, _ := body["target"].(map[string]any)
	if target == nil {
		target = map[string]any{}
	}

	rawAllow := body["allowlist"]
	if !pyTruthy(rawAllow) {
		rawAllow = []any{}
	}
	allowList, ok := rawAllow.([]any)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "allowlist must be a list"})
		return
	}
	allow := make([]string, 0, len(allowList))
	for _, item := range allowList {
		allow = append(allow, pyStr(item))
	}

	var classifyAfter *float64
	if raw, present := body["classify_after_seconds"]; present && raw != nil {
		parsed, ok := toFloat(raw)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "classify_after_seconds must be a number or null"})
			return
		}
		if parsed <= 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "classify_after_seconds must be positive"})
			return
		}
		classifyAfter = &parsed
	}

	err = deps.Store.UpdateNotificationsConfig(colonyID, SentinelNotificationsUpdate{
		SentinelEnabled:      enabled,
		Channel:              sentinelOptionalString(channel),
		Target:               target,
		Allowlist:            allow,
		ClassifyAfterSeconds: classifyAfter,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Colony '%s' not found", colonyID)})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}

	if deps.RefreshListeners != nil {
		safeCall(deps.RefreshListeners)
	}

	cfg := deps.Store.LoadNotificationsConfig(colonyID)
	writeJSON(w, http.StatusOK, sentinelConfigPayload(colonyID, cfg, deps.Notifier))
}

func handleTestNotification(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	colonyID := r.PathValue("colony_id")
	if validateColonyName(colonyID) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid colony_id"})
		return
	}
	if sentinelColonyMissing(deps.Home, colonyID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": fmt.Sprintf("Colony '%s' not found", colonyID)})
		return
	}
	if deps.Store == nil {
		sentinelStoreUnavailable(w)
		return
	}
	cfg := deps.Store.LoadNotificationsConfig(colonyID)

	body := map[string]any{}
	if value, err := decodeJSONValue(r); err == nil {
		if object, ok := value.(map[string]any); ok {
			body = object
		}
	}
	channel := pyStrOrEmpty(body["channel"])
	if channel == "" {
		channel = cfg.Channel
	}
	target, _ := body["target"].(map[string]any)
	if len(target) == 0 {
		target = cfg.Target
	}
	if !sentinelValidChannels[channel] || len(target) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "channel and target required to send a test"})
		return
	}

	if deps.Notifier == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "message_id": nil, "error": "sentinel notifier not configured"})
		return
	}
	text := fmt.Sprintf("✅ Sentinel test for colony %q. If you can read this, replies will reach the queen.", colonyID)
	result, err := deps.Notifier.Send(channel, target, text, nil)
	if err != nil {
		result = SentinelSendResult{OK: false, Error: err.Error()}
	}
	var messageID any
	if result.MessageID != "" {
		messageID = result.MessageID
	}
	var sendError any
	if result.Error != "" {
		sendError = result.Error
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         result.OK,
		"message_id": messageID,
		"error":      sendError,
	})
}

func handleSentinelCredentialStatus(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	writeJSON(w, http.StatusOK, sentinelChannelStatus(deps.Notifier))
}

// sentinelHTTPJSON issues a request and decodes a JSON object response. The
// python handlers treat any transport/parse failure as an in-band ok=false.
func sentinelHTTPJSON(client *http.Client, request *http.Request) (map[string]any, error) {
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	value, err := decodeJSONBytes(data)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("response is not a JSON object")
	}
	return object, nil
}

// Parity: framework/server/routes_sentinel.py::handle_telegram_validate
func handleTelegramValidate(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	body := map[string]any{}
	if value, err := decodeJSONValue(r); err == nil {
		if object, ok := value.(map[string]any); ok {
			body = object
		}
	}
	token := pyStrOrEmpty(body["token"])
	if token == "" && deps.Notifier != nil {
		token = deps.Notifier.TelegramToken()
	}
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no token provided or saved"})
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		deps.TelegramAPIBase+"/bot"+token+"/getMe", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	data, err := sentinelHTTPJSON(deps.HTTPClient, request)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !pyTruthy(data["ok"]) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": pyStrOr(data["description"], "invalid token")})
		return
	}
	result, _ := data["result"].(map[string]any)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"bot_username": result["username"],
		"bot_name":     result["first_name"],
	})
}

// sentinelStringID renders a Telegram id the way python's str() does, but
// keeps None as nil so the JSON field is null rather than "None".
func sentinelStringID(value any) any {
	if value == nil {
		return nil
	}
	return pyStr(value)
}

// Parity: framework/server/routes_sentinel.py::handle_telegram_detect_chat
func handleTelegramDetectChat(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	token := ""
	if deps.Notifier != nil {
		token = deps.Notifier.TelegramToken()
	}
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "save the bot token first"})
		return
	}

	query := url.Values{}
	query.Set("offset", "-1")
	query.Set("limit", "1")
	query.Set("allowed_updates", `["message"]`)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		deps.TelegramAPIBase+"/bot"+token+"/getUpdates?"+query.Encode(), nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	data, err := sentinelHTTPJSON(deps.HTTPClient, request)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !pyTruthy(data["ok"]) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": pyStrOr(data["description"], "getUpdates failed")})
		return
	}
	updates, _ := data["result"].([]any)
	if len(updates) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"pending": true,
			"error":   "No message yet — send any message to your bot, then retry.",
		})
		return
	}
	update, _ := updates[len(updates)-1].(map[string]any)
	message, _ := update["message"].(map[string]any)
	chat, _ := message["chat"].(map[string]any)
	sender, _ := message["from"].(map[string]any)

	chatTitle := any(nil)
	for _, key := range []string{"title", "first_name", "username"} {
		if value, ok := chat[key]; ok && pyTruthy(value) {
			chatTitle = value
			break
		}
	}
	senderName := any(nil)
	for _, key := range []string{"username", "first_name"} {
		if value, ok := sender[key]; ok && pyTruthy(value) {
			senderName = value
			break
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"chat_id":     sentinelStringID(chat["id"]),
		"chat_title":  chatTitle,
		"sender_id":   sentinelStringID(sender["id"]),
		"sender_name": senderName,
	})
}

// Parity: framework/server/routes_sentinel.py::handle_slack_validate
func handleSlackValidate(w http.ResponseWriter, r *http.Request, deps SentinelRouteDeps) {
	body := map[string]any{}
	if value, err := decodeJSONValue(r); err == nil {
		if object, ok := value.(map[string]any); ok {
			body = object
		}
	}
	token := pyStrOrEmpty(body["token"])
	if token == "" && deps.Notifier != nil {
		token = deps.Notifier.SlackBotToken()
	}
	if token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "no token provided or saved"})
		return
	}

	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, deps.SlackAPIBase+"/auth.test", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	request.Header.Set("Authorization", "Bearer "+token)
	data, err := sentinelHTTPJSON(deps.HTTPClient, request)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !pyTruthy(data["ok"]) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": pyStrOr(data["error"], "invalid token")})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"team":     data["team"],
		"bot_user": data["user"],
	})
}
