package browserbridge

import (
	"encoding/json"
	"time"
)

// events_consume.go ports the stateful half of bridge.py's
// _handle_cdp_event: telemetry rows, main-frame health invalidation, and the
// per-tab pending-dialog tracker. No new wire strings are introduced — the
// extension's forwarded method names are matched verbatim.

func (b *Bridge) handleCDPEvent(browserProfile string, tabID int, method string, params map[string]any) {
	profile := browserProfile
	if owner := b.tabOwnerOf(tabID); owner != "" {
		profile = owner
	}
	logWriter, hasLog := b.telemetry.(EventLogger)

	if method == "Runtime.consoleAPICalled" {
		args, _ := params["args"].([]any)
		var first, payload any
		if len(args) > 0 {
			if m, ok := args[0].(map[string]any); ok {
				first = m["value"]
			}
		}
		if len(args) > 1 {
			if m, ok := args[1].(map[string]any); ok {
				payload = m["value"]
			}
		}
		if first == "[hive_vp]" {
			if text, ok := payload.(string); ok {
				entry := map[string]any{"type": "viewport_event", "tab_id": tabID, "profile": profile}
				var parsed map[string]any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					for k, v := range parsed {
						entry[k] = v
					}
				} else {
					entry["_raw"] = text
				}
				if hasLog {
					logWriter.LogEvent(entry)
				}
			}
			return
		}
		if first == "[hive_attach_canary]" {
			if text, ok := payload.(string); ok {
				entry := map[string]any{"type": "attach_canary", "tab_id": tabID, "profile": profile}
				var parsed map[string]any
				if json.Unmarshal([]byte(text), &parsed) == nil {
					for k, v := range parsed {
						entry[k] = v
					}
				} else {
					entry["_raw"] = text
				}
				if hasLog {
					logWriter.LogEvent(entry)
				}
			}
			return
		}
		if hasLog {
			compact := make([]string, 0, 4)
			for i, arg := range args {
				if i >= 4 {
					break
				}
				m, ok := arg.(map[string]any)
				if !ok {
					continue
				}
				switch v := m["value"].(type) {
				case string:
					compact = append(compact, truncateLine(v, 120))
				case nil:
				default:
					compact = append(compact, truncateLine(toPlainString(v), 120))
				}
			}
			logWriter.LogEvent(map[string]any{
				"type":    "cdp_event",
				"tab_id":  tabID,
				"method":  method,
				"level":   params["type"],
				"args":    compact,
				"profile": profile,
			})
		}
		return
	}

	if hasLog {
		logWriter.LogEvent(map[string]any{
			"type":    "cdp_event",
			"tab_id":  tabID,
			"method":  method,
			"params":  truncateAny(params, 500),
			"profile": profile,
		})
	}

	switch method {
	case "Page.frameNavigated":
		frame, _ := params["frame"].(map[string]any)
		if tabID > 0 && frame != nil {
			if parent, _ := frame["parentId"].(string); parent == "" {
				// New document → URL/iframe-driven health rules are stale.
				b.clearTabHealth(tabID)
			}
		}
	case "Page.javascriptDialogOpening":
		if tabID > 0 {
			b.setPendingDialog(tabID, map[string]any{
				"type":           params["type"],
				"message":        params["message"],
				"default_prompt": params["defaultPrompt"],
				"url":            params["url"],
				"opened_at_ms":   time.Now().UnixMilli(),
			})
		}
	case "Page.javascriptDialogClosed":
		if tabID > 0 {
			b.clearPendingDialog(tabID)
		}
	}
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func toPlainString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		return string(data)
	}
}

// dialogSnapshot is used by the pending-dialog read commands.
func (b *Bridge) dialogSnapshot(tabID int) map[string]any {
	d := b.PendingDialog(tabID)
	if d == nil {
		return nil
	}
	return map[string]any{
		"tab_id":         tabID,
		"type":           d["type"],
		"message":        d["message"],
		"default_prompt": d["default_prompt"],
		"url":            d["url"],
		"opened_at_ms":   d["opened_at_ms"],
	}
}
