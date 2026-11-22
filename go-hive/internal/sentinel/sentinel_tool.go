package sentinel

// Sentinel notification-setup tool for the queen — the Go port of
// framework/agent_loop/internals/sentinel_tool.py.
//
// Exposes the same operations the desktop Sentinel UI performs, so the queen
// can finish a browser-driven Slack / Telegram setup end to end instead of
// bouncing the user back to the connector form:
//
//   - store_token — save a channel token to the encrypted credential store
//     (the UI's "Connect" step: credential ids slack / slack_app / telegram).
//     Slack and Telegram tokens are validated before they're stored, mirroring
//     the UI's validate-then-save.
//   - configure   — write the per-colony notifications.json (channel, target,
//     allowlist, enable) — the UI's "Save" step — and start the inbound
//     listener so two-way replies work without a runtime restart.
//   - test        — send a test message to the configured destination.
//   - status      — report which tokens are stored + the colony's config.
//
// Single-tool-with-action shape mirrors the credentials tool. All work is
// in-process and writes to the same encrypted store + on-disk config the
// Sentinel notifier and manager read, so the desktop UI reflects it.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/llm"
)

// SentinelSetupToolName is the model-visible name of the tool.
const SentinelSetupToolName = "sentinel_setup"

// _VALID_CHANNELS: the channels configure/test accept. The model schema only
// advertises slack/telegram, but upstream accepts the first-party hive channel
// too, so the validation set keeps it.
var validSetupChannels = map[string]bool{"hive": true, "telegram": true, "slack": true}

// _PROVIDER_IDS: credential id used by the Sentinel notifier per provider.
var validSetupProviders = map[string]bool{"slack": true, "slack_app": true, "telegram": true}

// SentinelSetupToolDescription is SENTINEL_TOOL_DESCRIPTION, byte-for-byte.
const SentinelSetupToolDescription = "Set up Sentinel notifications (Slack / Telegram) for a colony — the same actions the desktop Sentinel connector performs. Call with no args (or action=\"help\") for usage. Actions: help, status, store_token, detect_chat, configure, test. Use store_token to save a channel token you obtained (e.g. a Slack bot xoxb- / app-level xapp- token, or a Telegram bot token); detect_chat finds the Telegram chat id after the user messages the bot; configure sets the colony's channel + turns Sentinel on; test verifies a message lands. Tokens are stored encrypted; configure/test target the colony bound to this session unless you pass colony_id."

// sentinelSetupToolHelp is SENTINEL_TOOL_HELP, byte-for-byte.
const sentinelSetupToolHelp = "sentinel_setup — connect Slack/Telegram notifications (Sentinel) for a colony.\n\nThis performs the same steps as the desktop Sentinel connector, so you can finish\na browser-driven setup yourself instead of asking the user to paste tokens.\n\nActions:\n\n- status\n    Show which channel tokens are stored (telegram / slack bot / slack app) and,\n    if a colony is bound to this session, its current notifications config.\n\n- store_token  provider  token\n    Save a channel token to the encrypted credential store.\n      provider=\"slack\"      -> Slack Bot User OAuth Token (xoxb-…); validated via auth.test\n      provider=\"slack_app\"  -> Slack App-Level Token (xapp-…, connections:write)\n      provider=\"telegram\"   -> Telegram bot token (from @BotFather); validated via getMe\n    Read the token off the page after creating the bot/app, then store it.\n\n- detect_chat\n    (Telegram) After the user sends ANY message to the bot, find the chat to\n    notify. Returns the latest chat_id + the sender's id (use it for allowlist).\n    Returns PENDING if no message has arrived yet — ask the user to message the\n    bot, then retry. Requires the Telegram token to be stored first.\n\n- configure  channel  target  [enabled]  [allowlist]  [classify_after_seconds]  [colony_id]\n    Write the per-colony notifications config (the \"Save\" step).\n      channel=\"slack\",   target={\"channel\": \"C0123ABC\"}     (invite the bot to that channel first)\n      channel=\"telegram\", target={\"chat_id\": \"123456789\"}\n      enabled=true turns Sentinel on; allowlist = sender ids allowed to reply.\n\n- test  [channel]  [target]  [colony_id]\n    Send a test message to the configured (or supplied) destination.\n\nExamples:\n  sentinel_setup({\"action\": \"store_token\", \"provider\": \"slack\", \"token\": \"xoxb-…\"})\n  sentinel_setup({\"action\": \"store_token\", \"provider\": \"slack_app\", \"token\": \"xapp-…\"})\n  sentinel_setup({\"action\": \"configure\", \"channel\": \"slack\", \"target\": {\"channel\": \"C0123ABC\"}, \"enabled\": true})\n  sentinel_setup({\"action\": \"store_token\", \"provider\": \"telegram\", \"token\": \"123456:ABC-…\"})\n  sentinel_setup({\"action\": \"detect_chat\"})\n  sentinel_setup({\"action\": \"configure\", \"channel\": \"telegram\", \"target\": {\"chat_id\": \"123456789\"}, \"allowlist\": [\"123456789\"], \"enabled\": true})\n  sentinel_setup({\"action\": \"test\"})\n"

// RenderSentinelSetupHelp returns the fresh usage text, for action=help or no
// action. The literal above is copied byte-for-byte from SENTINEL_TOOL_HELP.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::render_help
func RenderSentinelSetupHelp() string { return sentinelSetupToolHelp }

// SentinelSetupToolDef builds the model-visible sentinel_setup definition.
// Description and input schema are SENTINEL_TOOL_DESCRIPTION and the
// parameters dict of build_sentinel_setup_tool, verbatim (JSON Schema types:
// every enum is a string array, allowlist items are strings, and
// classify_after_seconds is a number).
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::build_sentinel_setup_tool
func SentinelSetupToolDef() llm.ToolDef {
	return llm.ToolDef{
		Name:        SentinelSetupToolName,
		Description: SentinelSetupToolDescription,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []any{"help", "status", "store_token", "detect_chat", "configure", "test"},
					"description": "What to do. Defaults to \"help\", which returns full usage.",
				},
				"provider": map[string]any{
					"type":        "string",
					"enum":        []any{"slack", "slack_app", "telegram"},
					"description": "For store_token: which token. 'slack' = bot xoxb-, 'slack_app' = app-level xapp-, 'telegram' = bot token.",
				},
				"token": map[string]any{
					"type":        "string",
					"description": "For store_token: the raw token value to store (kept encrypted).",
				},
				"channel": map[string]any{
					"type":        "string",
					"enum":        []any{"slack", "telegram"},
					"description": "For configure/test: the channel to deliver on.",
				},
				"target": map[string]any{
					"type":        "object",
					"description": "For configure/test: destination. Slack: {\"channel\": \"C0123ABC\"}. Telegram: {\"chat_id\": \"123456789\"}.",
				},
				"enabled": map[string]any{
					"type":        "boolean",
					"description": "For configure: turn Sentinel on (true) or off (false) for the colony.",
				},
				"allowlist": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "For configure: sender ids allowed to reply and drive the colony.",
				},
				"classify_after_seconds": map[string]any{
					"type":        "number",
					"description": "For configure: per-colony idle budget before escalating; omit to inherit the default.",
				},
				"colony_id": map[string]any{
					"type":        "string",
					"description": "For configure/test: target colony's on-disk directory name. Defaults to the colony bound to this session.",
				},
			},
			"required": []any{},
		},
	}
}

// SentinelSetupManager is the slice of the process-global Sentinel manager the
// tool reads (detect_chat) and pokes (configure → refresh_listeners).
type SentinelSetupManager interface {
	RecentChat(channel string, maxAge ...float64) map[string]any
	IsListening(channel string) bool
	RefreshListeners()
}

// SentinelSetupDeps carries the tool's runtime seams. Production assembles
// them in internal/cli; tests inject fakes so no real notification is sent.
type SentinelSetupDeps struct {
	// Store reads/writes the per-colony notifications.json. Nil reads as an
	// unconfigured colony and refuses configure.
	Store *Store
	// Notifier supplies the stored-token view, the test send, and the
	// Telegram token detect_chat needs. Nil means "no notifier configured".
	Notifier *HTTPNotifier
	// Manager is the process-global Sentinel manager (nil before it starts).
	Manager SentinelSetupManager
	// SaveToken persists one channel token under its credential id (slack /
	// slack_app / telegram). Nil returns an actionable tool error.
	SaveToken func(credentialID, token string) error
	// PublishRefresh nudges the desktop UI after a successful store/configure.
	PublishRefresh func()
	// HTTP issues the token-validation and getUpdates requests. Nil means a
	// 15s-timeout client, matching upstream's httpx timeout.
	HTTP HTTPDoer
	// TelegramAPIBase / SlackAPIBase override the provider hosts (tests).
	TelegramAPIBase string
	SlackAPIBase    string
}

func setupHTTPDoer(deps SentinelSetupDeps) HTTPDoer {
	if deps.HTTP != nil {
		return deps.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func setupTelegramBase(deps SentinelSetupDeps) string {
	if base := strings.TrimSuffix(deps.TelegramAPIBase, "/"); base != "" {
		return base
	}
	return defaultTelegramAPI
}

func setupSlackBase(deps SentinelSetupDeps) string {
	if base := strings.TrimSuffix(deps.SlackAPIBase, "/"); base != "" {
		return base
	}
	return defaultSlackAPI
}

// HandleSentinelSetup resolves a sentinel_setup call and returns the result
// text. defaultColonyID is the colony bound to the current session (resolved
// by the caller from the live colony runtime); configure/test fall back to it
// when colony_id isn't passed explicitly.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::handle
func HandleSentinelSetup(ctx context.Context, toolInput map[string]any, defaultColonyID string, deps SentinelSetupDeps) string {
	ci := toolInput
	if ci == nil {
		ci = map[string]any{}
	}
	// action = str(ci.get("action", "") or "help").strip().lower()
	action := "help"
	if raw, present := ci["action"]; present && PyTruthy(raw) {
		action = strings.ToLower(strings.TrimSpace(PyStr(raw)))
	}

	if action == "" || action == "help" {
		return RenderSentinelSetupHelp()
	}
	switch action {
	case "status":
		return sentinelSetupStatus(deps, defaultColonyID)
	case "store_token":
		result := sentinelSetupStoreToken(ctx, ci, deps)
		if !strings.HasPrefix(result, "ERROR:") {
			sentinelSetupPublishRefresh(deps)
		}
		return result
	case "detect_chat":
		return sentinelSetupDetectChat(ctx, deps)
	case "configure":
		result := sentinelSetupConfigure(ci, defaultColonyID, deps)
		if !strings.HasPrefix(result, "ERROR:") {
			sentinelSetupPublishRefresh(deps)
		}
		return result
	case "test":
		return sentinelSetupTest(ctx, ci, defaultColonyID, deps)
	default:
		return fmt.Sprintf("ERROR: unknown sentinel_setup action '%s'. Call sentinel_setup() for usage.", action)
	}
}

// ----- token storage + validation -----------------------------------------

// sentinelSetupStoreToken implements the store_token action.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_store_token
func sentinelSetupStoreToken(ctx context.Context, ci map[string]any, deps SentinelSetupDeps) string {
	// provider = str(ci.get("provider", "") or "").strip().lower()
	provider := ""
	if raw, present := ci["provider"]; present && PyTruthy(raw) {
		provider = strings.ToLower(strings.TrimSpace(PyStr(raw)))
	}
	// token = str(ci.get("token", "") or "").strip()
	token := ""
	if raw, present := ci["token"]; present && PyTruthy(raw) {
		token = strings.TrimSpace(PyStr(raw))
	}
	if !validSetupProviders[provider] {
		return "ERROR: provider must be 'slack' (bot xoxb-), 'slack_app' (app-level xapp-), or 'telegram'."
	}
	if token == "" {
		return "ERROR: token is required."
	}

	switch provider {
	case "slack":
		if !strings.HasPrefix(token, "xoxb-") {
			return "ERROR: a Slack bot token starts with 'xoxb-'. For the app-level token use provider='slack_app'."
		}
		ok, team, errText := validateSlackSetupToken(ctx, token, deps)
		if !ok {
			return "ERROR: Slack rejected the bot token (auth.test): " + errText
		}
		if err := saveSetupToken(deps, "slack", token); err != nil {
			return "ERROR: could not store the token: " + err.Error()
		}
		if team == "" {
			team = "unknown"
		}
		return "Stored Slack bot token — connected to workspace '" + team + "'. Sentinel can now send to Slack."
	case "slack_app":
		if !strings.HasPrefix(token, "xapp-") {
			return "ERROR: a Slack app-level token starts with 'xapp-'."
		}
		if err := saveSetupToken(deps, "slack_app", token); err != nil {
			return "ERROR: could not store the token: " + err.Error()
		}
		return "Stored Slack app-level token. Sentinel can now receive replies over Socket Mode."
	default: // telegram
		ok, who, errText := validateTelegramSetupToken(ctx, token, deps)
		if !ok {
			return "ERROR: Telegram rejected the token (getMe): " + errText
		}
		if err := saveSetupToken(deps, "telegram", token); err != nil {
			return "ERROR: could not store the token: " + err.Error()
		}
		if who != "" {
			return "Stored Telegram bot token (@" + who + ")."
		}
		return "Stored Telegram bot token."
	}
}

func saveSetupToken(deps SentinelSetupDeps, credentialID, token string) error {
	if deps.SaveToken == nil {
		return errors.New("credential store unavailable")
	}
	return deps.SaveToken(credentialID, token)
}

// validateSlackSetupToken checks the bot token via Slack auth.test — the same
// check as the desktop connector.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_validate_slack
func validateSlackSetupToken(ctx context.Context, token string, deps SentinelSetupDeps) (bool, string, string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, setupSlackBase(deps)+"/auth.test", nil)
	if err != nil {
		return false, "", err.Error()
	}
	request.Header.Set("Authorization", "Bearer "+token)
	data, err := sentinelSetupJSONRequest(setupHTTPDoer(deps), request)
	if err != nil {
		return false, "", err.Error()
	}
	if !PyTruthy(data["ok"]) {
		errText := "invalid token"
		if value, present := data["error"]; present {
			errText = PyStr(value)
		}
		return false, "", errText
	}
	// data.get("team") — None/absent resolves to "" and the caller renders
	// "unknown"; a present value is str()'d like the f-string does.
	team := ""
	if value, present := data["team"]; present && value != nil {
		team = PyStr(value)
	}
	return true, team, ""
}

// validateTelegramSetupToken checks the bot token via getMe — the same check
// as the desktop connector.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_validate_telegram
func validateTelegramSetupToken(ctx context.Context, token string, deps SentinelSetupDeps) (bool, string, string) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, setupTelegramBase(deps)+"/bot"+token+"/getMe", nil)
	if err != nil {
		return false, "", err.Error()
	}
	data, err := sentinelSetupJSONRequest(setupHTTPDoer(deps), request)
	if err != nil {
		return false, "", err.Error()
	}
	if !PyTruthy(data["ok"]) {
		errText := "invalid token"
		if value, present := data["description"]; present {
			errText = PyStr(value)
		}
		return false, "", errText
	}
	result, _ := data["result"].(map[string]any)
	who := ""
	if result != nil {
		// res.get("username") or res.get("first_name")
		for _, key := range []string{"username", "first_name"} {
			if value, present := result[key]; present && PyTruthy(value) {
				who = PyStr(value)
				break
			}
		}
	}
	return true, who, ""
}

// ----- detect_chat --------------------------------------------------------

// formatDetectedSetupChat renders the detected-chat result.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_format_detected
func formatDetectedSetupChat(chatID, chatTitle, senderID, senderName any) string {
	title := "chat"
	if PyTruthy(chatTitle) {
		title = PyStr(chatTitle)
	}
	name := "user"
	if PyTruthy(senderName) {
		name = PyStr(senderName)
	}
	return fmt.Sprintf(
		`Detected Telegram chat "%s": chat_id=%s; sender=%s (id %s). Configure with target={"chat_id": "%s"} and allowlist=["%s"], then enable.`,
		title, PyStr(chatID), name, PyStr(senderID), PyStr(chatID), PyStr(senderID),
	)
}

// sentinelSetupDetectChat finds the Telegram chat to notify after the user
// messages the bot.
//
// Two paths, because the running Sentinel daemon long-polls getUpdates and
// ACKS each update (advancing the offset) — a competing getUpdates here comes
// back empty whenever a listener is running. When the daemon is listening we
// read the chat IT already saw; only when nothing is consuming updates do we
// poll the API ourselves.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_detect_chat
func sentinelSetupDetectChat(ctx context.Context, deps SentinelSetupDeps) string {
	token := ""
	if deps.Notifier != nil {
		token = deps.Notifier.TelegramToken()
	}
	if token == "" {
		return "ERROR: store the Telegram bot token first (store_token provider=telegram)."
	}

	// Path 1 — a Sentinel listener owns the update stream: read its cache.
	if deps.Manager != nil {
		recent := deps.Manager.RecentChat("telegram")
		if PyTruthy(recent["chat_id"]) {
			return formatDetectedSetupChat(recent["chat_id"], recent["chat_title"], recent["sender_id"], recent["sender_name"])
		}
		if deps.Manager.IsListening("telegram") {
			return "PENDING: the Sentinel Telegram listener is running but hasn't seen a message yet. " +
				"Ask the user to open the bot and tap Start (or send any message), then run detect_chat again. " +
				"Don't decrypt tokens or poll the Telegram API by hand — the listener owns the update stream and will " +
				"surface the chat here."
		}
	}

	// Path 2 — nothing is consuming updates: poll getUpdates ourselves.
	query := url.Values{}
	query.Set("offset", "-1")
	query.Set("limit", "1")
	query.Set("allowed_updates", `["message"]`)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		setupTelegramBase(deps)+"/bot"+token+"/getUpdates?"+query.Encode(), nil)
	if err != nil {
		return "ERROR: getUpdates failed: " + err.Error()
	}
	data, err := sentinelSetupJSONRequest(setupHTTPDoer(deps), request)
	if err != nil {
		return "ERROR: getUpdates failed: " + err.Error()
	}
	if !PyTruthy(data["ok"]) {
		errText := "unknown"
		if value, present := data["description"]; present {
			errText = PyStr(value)
		}
		return "ERROR: getUpdates rejected: " + errText
	}
	updates, _ := data["result"].([]any)
	if len(updates) == 0 {
		return "PENDING: no message yet — ask the user to open the bot in Telegram and send it any message (or tap Start), then run detect_chat again."
	}
	update, _ := updates[len(updates)-1].(map[string]any)
	message, _ := update["message"].(map[string]any)
	chat, _ := message["chat"].(map[string]any)
	sender, _ := message["from"].(map[string]any)
	var chatID any
	if chat != nil {
		chatID = chat["id"]
	}
	if chatID == nil {
		return "PENDING: latest update has no chat — send a direct message to the bot and retry."
	}
	var title any
	for _, key := range []string{"title", "first_name", "username"} {
		if value, present := chat[key]; present && PyTruthy(value) {
			title = value
			break
		}
	}
	var senderName any
	for _, key := range []string{"username", "first_name"} {
		if value, present := sender[key]; present && PyTruthy(value) {
			senderName = value
			break
		}
	}
	return formatDetectedSetupChat(chatID, title, sender["id"], senderName)
}

// ----- per-colony config + test ------------------------------------------

// sentinelSetupStatus reports which tokens are stored plus the bound colony's
// notifications config.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_status
func sentinelSetupStatus(deps SentinelSetupDeps, defaultColonyID string) string {
	tokenState := func(value string) string {
		if value != "" {
			return "configured"
		}
		return "not set"
	}
	telegram, slackBot, slackApp := "", "", ""
	if deps.Notifier != nil {
		telegram = deps.Notifier.TelegramToken()
		slackBot = deps.Notifier.SlackBotToken()
		slackApp = deps.Notifier.SlackAppToken()
	}
	lines := []string{
		"Sentinel channel tokens:",
		"  telegram        : " + tokenState(telegram),
		"  slack bot (xoxb): " + tokenState(slackBot),
		"  slack app (xapp): " + tokenState(slackApp),
		"",
	}
	if defaultColonyID != "" {
		cfg := setupNotificationsConfig(deps, defaultColonyID)
		channel := "-"
		if PyTruthy(cfg.Channel) {
			channel = PyStr(cfg.Channel)
		}
		target := any(cfg.Target)
		if !PyTruthy(target) {
			target = map[string]any{}
		}
		allowlist := any(cfg.Allowlist)
		if !PyTruthy(allowlist) {
			allowlist = []string{}
		}
		lines = append(lines, fmt.Sprintf(
			"Colony '%s': enabled=%s channel=%s target=%s allowlist=%s",
			defaultColonyID, PyStr(cfg.SentinelEnabled), channel, pyReprValue(target), pyReprValue(allowlist),
		))
	} else {
		lines = append(lines, "No colony bound to this session — pass colony_id to configure/test.")
	}
	return strings.Join(lines, "\n")
}

func setupNotificationsConfig(deps SentinelSetupDeps, colonyID string) NotificationsConfig {
	if deps.Store == nil {
		return NotificationsConfig{
			SentinelEnabled: true,
			Channel:         DefaultChannel,
			Target:          map[string]any{},
			Allowlist:       []string{},
		}
	}
	return deps.Store.LoadNotificationsConfig(colonyID)
}

// sentinelSetupConfigure writes the per-colony notifications config and starts
// the inbound listener so two-way replies work without a restart.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_configure
func sentinelSetupConfigure(ci map[string]any, defaultColonyID string, deps SentinelSetupDeps) string {
	// cid = str(ci.get("colony_id", "") or "").strip() or default_colony_id
	cid := ""
	if raw, present := ci["colony_id"]; present && PyTruthy(raw) {
		cid = strings.TrimSpace(PyStr(raw))
	}
	if cid == "" {
		cid = defaultColonyID
	}
	if cid == "" {
		return "ERROR: no colony bound to this session — pass colony_id (the colony's on-disk directory name)."
	}

	channelRaw := ci["channel"]
	enabled := PyTruthy(ci["enabled"])
	if enabled && !isValidSetupChannel(channelRaw) {
		return "ERROR: channel must be 'telegram' or 'slack' when enabled=true."
	}

	target, _ := ci["target"].(map[string]any)
	if target == nil {
		target = map[string]any{}
	}
	allow, allowErr := setupAllowlist(ci["allowlist"])
	if allowErr != "" {
		return allowErr
	}

	var classifyAfter *float64
	if raw, present := ci["classify_after_seconds"]; present && raw != nil {
		value, ok := pyFloat(raw)
		if !ok {
			return "ERROR: classify_after_seconds must be a number or null."
		}
		if value <= 0 {
			return "ERROR: classify_after_seconds must be positive."
		}
		classifyAfter = &value
	}

	// channel is persisted as-is upstream; the schema type is string, so a
	// non-string value persists JSON null here (the store's resolved channel
	// falls back to "hive" in both runtimes).
	var channelPtr *string
	if channel, ok := channelRaw.(string); ok {
		channelPtr = &channel
	}
	if deps.Store == nil {
		return fmt.Sprintf("ERROR: colony '%s' not found on disk.", cid)
	}
	err := deps.Store.UpdateNotificationsConfig(cid, NotificationsUpdate{
		SentinelEnabled:      enabled,
		Channel:              channelPtr,
		Target:               target,
		Allowlist:            allow,
		ClassifyAfterSeconds: classifyAfter,
	})
	if errors.Is(err, ErrColonyNotFound) {
		return fmt.Sprintf("ERROR: colony '%s' not found on disk.", cid)
	}
	if err != nil {
		return "ERROR: could not save the Sentinel config: " + err.Error()
	}

	// Start the inbound listener now so two-way replies work without a
	// restart. Best-effort: a refresh failure must not fail the save.
	if deps.Manager != nil {
		safeSetupCall(deps.Manager.RefreshListeners)
	}

	state := "disabled"
	if enabled {
		state = "ENABLED"
	}
	channelLabel := "-"
	if PyTruthy(channelRaw) {
		channelLabel = PyStr(channelRaw)
	}
	targetLabel := any(target)
	if !PyTruthy(targetLabel) {
		targetLabel = map[string]any{}
	}
	return fmt.Sprintf(
		"Saved Sentinel config for colony '%s': %s, channel=%s, target=%s.",
		cid, state, channelLabel, pyReprValue(targetLabel),
	)
}

func isValidSetupChannel(raw any) bool {
	channel, ok := raw.(string)
	return ok && validSetupChannels[channel]
}

func setupAllowlist(raw any) ([]string, string) {
	// allow = ci.get("allowlist") or []
	allow, isList := raw.([]any)
	if !PyTruthy(raw) {
		return []string{}, ""
	}
	if !isList {
		return nil, "ERROR: allowlist must be a list."
	}
	out := make([]string, 0, len(allow))
	for _, item := range allow {
		out = append(out, PyStr(item))
	}
	return out, ""
}

// sentinelSetupTest sends a test message to the configured (or supplied)
// destination.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_test
func sentinelSetupTest(ctx context.Context, ci map[string]any, defaultColonyID string, deps SentinelSetupDeps) string {
	cid := ""
	if raw, present := ci["colony_id"]; present && PyTruthy(raw) {
		cid = strings.TrimSpace(PyStr(raw))
	}
	if cid == "" {
		cid = defaultColonyID
	}
	var cfg *NotificationsConfig
	if cid != "" {
		loaded := setupNotificationsConfig(deps, cid)
		cfg = &loaded
	}
	channelRaw := ci["channel"]
	if !PyTruthy(channelRaw) && cfg != nil {
		channelRaw = cfg.Channel
	}
	targetRaw := ci["target"]
	if !PyTruthy(targetRaw) && cfg != nil {
		targetRaw = cfg.Target
	}
	channel := ""
	if value, ok := channelRaw.(string); ok {
		channel = value
	}
	target, targetIsMap := targetRaw.(map[string]any)
	if !validSetupChannels[channel] || !PyTruthy(targetRaw) || !targetIsMap {
		return "ERROR: channel and target are required to send a test (pass them, or configure the colony first)."
	}

	label := cid
	if label == "" {
		label = "this colony"
	}
	if deps.Notifier == nil {
		return "ERROR: test send failed: sentinel notifier not configured"
	}
	result := deps.Notifier.Send(ctx, channel, target,
		"✅ Sentinel test for colony \""+label+"\". If you can read this, replies will reach the queen.",
		nil, nil)
	if result.OK {
		return fmt.Sprintf("Test message delivered to %s %s.", channel, pyReprValue(target))
	}
	return "ERROR: test send failed: " + result.Error
}

// ----- shared helpers -----------------------------------------------------

// sentinelSetupPublishRefresh nudges the desktop UI to refetch Sentinel state
// after an out-of-band store/configure (the same tool_catalog_refreshed global
// event routes_credentials pairs with a human credential save). Best-effort:
// a publish failure never turns a successful store/configure into a tool error.
//
// Parity: framework/agent_loop/internals/sentinel_tool.py::_publish_refresh
func sentinelSetupPublishRefresh(deps SentinelSetupDeps) {
	if deps.PublishRefresh == nil {
		return
	}
	safeSetupCall(deps.PublishRefresh)
}

// safeSetupCall runs a best-effort side effect and swallows a panic, matching
// the try/except-log block upstream wraps around listener refresh and event
// publishing.
func safeSetupCall(fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			sentinelLogf("sentinel_setup: side effect failed: %v", recovered)
		}
	}()
	fn()
}

// sentinelSetupJSONRequest issues a request and decodes a JSON object body.
func sentinelSetupJSONRequest(client HTTPDoer, request *http.Request) (map[string]any, error) {
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty response body")
	}
	data := map[string]any{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, err
	}
	return data, nil
}

// pyFloat mirrors Python float() for the value types a decoded JSON input can
// carry (numeric strings included, as upstream accepts them).
func pyFloat(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case bool:
		if typed {
			return 1, true
		}
		return 0, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

// pyReprValue renders a decoded JSON value the way Python's repr() does for
// scalars, lists and dicts (sorted keys: Go maps have no insertion order).
func pyReprValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case string:
		return pyReprString(typed)
	case []string:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, pyReprString(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case []any:
		items := make([]string, 0, len(typed))
		for _, item := range typed {
			items = append(items, pyReprValue(item))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]string, 0, len(keys))
		for _, key := range keys {
			items = append(items, pyReprString(key)+": "+pyReprValue(typed[key]))
		}
		return "{" + strings.Join(items, ", ") + "}"
	default:
		return PyStr(value)
	}
}

// pyReprString renders a string as Python repr() does: single quotes unless
// the value contains one and no double quote, with escapes.
func pyReprString(value string) string {
	quote := byte('\'')
	if strings.ContainsRune(value, '\'') && !strings.ContainsRune(value, '"') {
		quote = '"'
	}
	var builder strings.Builder
	builder.WriteByte(quote)
	for _, char := range value {
		switch char {
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			if char == rune(quote) {
				builder.WriteByte('\\')
			}
			builder.WriteRune(char)
		}
	}
	builder.WriteByte(quote)
	return builder.String()
}
