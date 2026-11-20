package browserbridge

// control_dispatch.go maps the upstream RPC method names (bridge_rpc.py
// RPC_METHODS) onto the Go bridge surface.
//
// Each adapter mirrors the Python method's positional signature, so an
// out-of-process client written against bridge.py can pass args=[...] and
// params={...} unchanged. Adapters translate upstream snake_case names into
// the camelCase keys the Go command handlers already read, then reuse the same
// code path the HTTP /command surface uses.

import (
	"context"
	"fmt"
	"time"
)

// rpcFunc is one allowlisted method's adapter.
type rpcFunc func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error)

// rpcParams merges positional args (named by their upstream parameter names)
// with the keyword params. Keyword values win, mirroring a caller that passes
// the same value twice.
func rpcParams(args []any, params map[string]any, names ...string) map[string]any {
	out := make(map[string]any, len(names)+len(params))
	for i, name := range names {
		if i < len(args) {
			out[name] = args[i]
		}
	}
	for k, v := range params {
		out[k] = v
	}
	return out
}

func rpcTabID(args []any, params map[string]any) (int, error) {
	var raw any
	if len(args) > 0 {
		raw = args[0]
	} else {
		raw = params["tab_id"]
		if raw == nil {
			raw = params["tabId"]
		}
	}
	tabID, ok := toInt(raw)
	if !ok || tabID <= 0 {
		return 0, badRequest("'tab_id' must be a positive integer")
	}
	return tabID, nil
}

func rpcString(params map[string]any, key string) (string, error) {
	value, ok := params[key]
	if !ok || value == nil {
		return "", badRequest("missing required parameter '%s'", key)
	}
	text, isString := value.(string)
	if !isString {
		return "", badRequest("'%s' must be a string", key)
	}
	return text, nil
}

func rpcFloat(params map[string]any, key string) (float64, error) {
	value, ok := params[key]
	if !ok || value == nil {
		return 0, badRequest("missing required numeric parameter '%s'", key)
	}
	number, isNumber := toFloat(value)
	if !isNumber {
		return 0, badRequest("'%s' must be a number", key)
	}
	return number, nil
}

func rpcConnection(ctx context.Context, b *Bridge, tabID int) (*Connection, error) {
	conn, err := b.resolve("", &tabID)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func rpcMilliseconds(params map[string]any, key string, def int) time.Duration {
	ms := paramInt(params, key, def)
	if ms <= 0 {
		ms = def
	}
	return time.Duration(ms) * time.Millisecond
}

var rpcDispatchTable = map[string]rpcFunc{
	// ── contexts / lifecycle ────────────────────────────────────────────
	"create_context": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdCreateContext(ctx, b, "", rpcParams(args, params, "agent_id", "display_name", "browser_profile"))
	},
	"destroy_context": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdDestroyContext(ctx, b, "", rpcParams(args, params, "group_id", "browser_profile"))
	},
	"register_context": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdRegisterContext(ctx, b, "", rpcParams(args, params, "profile", "group_id", "name", "browser_profile"))
	},
	"list_contexts": func(ctx context.Context, b *Bridge, _ []any, _ map[string]any) (any, error) {
		return cmdListContexts(ctx, b, "", nil)
	},

	// ── tabs ────────────────────────────────────────────────────────────
	"create_tab": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdTabOpen(ctx, b, "", rpcParams(args, params, "url", "group_id"))
	},
	"close_tab": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdTabClose(ctx, b, "", map[string]any{"tabId": tabID})
	},
	"list_tabs": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdTabsList(ctx, b, "", rpcParams(args, params, "group_id"))
	},
	"activate_tab": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdBrowserActivateTab(ctx, b, "", map[string]any{"tab_id": tabID})
	},

	// ── navigation ──────────────────────────────────────────────────────
	"navigate": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdNavigate(ctx, b, "", rpcParams(args, params, "tab_id", "url", "wait_until", "timeout_ms"))
	},
	"go_back": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return b.GoBackHistory(ctx, tabID)
	},
	"go_forward": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return b.GoForwardHistory(ctx, tabID)
	},
	"reload": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdBrowserReload(ctx, b, "", map[string]any{"tab_id": tabID})
	},

	// ── interactions ────────────────────────────────────────────────────
	"click": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdClick(ctx, b, "", rpcParams(args, params, "tab_id", "selector", "button", "click_count", "timeout_ms"))
	},
	"click_coordinate": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdClick(ctx, b, "", rpcParams(args, params, "tab_id", "x", "y", "button", "click_count"))
	},
	"type_text": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdType(ctx, b, "", rpcParams(args, params,
			"tab_id", "selector", "text", "clear_first", "delay_ms", "timeout_ms", "use_insert_text"))
	},
	"press_key": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdKeypress(ctx, b, "", rpcParams(args, params, "tab_id", "key", "selector", "modifiers"))
	},
	"press_key_at": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "x", "y", "key")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		x, err := rpcFloat(merged, "x")
		if err != nil {
			return nil, err
		}
		y, err := rpcFloat(merged, "y")
		if err != nil {
			return nil, err
		}
		key, err := rpcString(merged, "key")
		if err != nil {
			return nil, err
		}
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		return b.pressKeyAt(ctx, conn, tabID, x, y, key)
	},
	"hover": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "selector", "timeout_ms")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		selector, err := rpcString(merged, "selector")
		if err != nil {
			return nil, err
		}
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		return b.hoverElement(ctx, conn, tabID, selector, rpcMilliseconds(merged, "timeout_ms", 30000))
	},
	"hover_coordinate": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "x", "y")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		x, err := rpcFloat(merged, "x")
		if err != nil {
			return nil, err
		}
		y, err := rpcFloat(merged, "y")
		if err != nil {
			return nil, err
		}
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		return b.hoverPoint(ctx, conn, tabID, x, y)
	},
	"scroll": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdScroll(ctx, b, "", rpcParams(args, params, "tab_id", "direction", "amount", "selector"))
	},
	"select_option": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdBrowserSelect(ctx, b, "", rpcParams(args, params, "tab_id", "selector", "values"))
	},

	// ── inspection ──────────────────────────────────────────────────────
	"screenshot": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "full_page", "selector", "timeout_s", "selector_timeout_ms")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdScreenshot(ctx, b, "", map[string]any{
			"tab_id":   tabID,
			"fullPage": paramBoolAny(merged, false, "full_page", "fullPage"),
			"selector": merged["selector"],
		})
	},
	"screenshot_region": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "x0", "y0", "x1", "y1", "scale", "timeout_s")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		var coords [4]float64
		for i, key := range []string{"x0", "y0", "x1", "y1"} {
			value, err := rpcFloat(merged, key)
			if err != nil {
				return nil, err
			}
			coords[i] = value
		}
		scale := paramFloat(merged, "scale", 2.0)
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		return b.captureRegion(ctx, conn, tabID, coords[0], coords[1], coords[2], coords[3], scale)
	},
	"snapshot": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdSnapshot(ctx, b, "", rpcParams(args, params, "tab_id", "timeout_s", "mode"))
	},
	"evaluate": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdEval(ctx, b, "", rpcParams(args, params, "tab_id", "script"))
	},
	"get_text": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdBrowserGetText(ctx, b, "", rpcParams(args, params, "tab_id", "selector", "timeout_ms"))
	},
	"get_attribute": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "selector", "attribute", "timeout_ms")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		selector, err := rpcString(merged, "selector")
		if err != nil {
			return nil, err
		}
		attribute, err := rpcString(merged, "attribute")
		if err != nil {
			return nil, err
		}
		return b.GetAttribute(ctx, tabID, selector, attribute, rpcMilliseconds(merged, "timeout_ms", 30000))
	},
	"shadow_query": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdBrowserShadowQuery(ctx, b, "", rpcParams(args, params, "tab_id", "selector"))
	},

	// ── advanced ────────────────────────────────────────────────────────
	"wait_for_selector": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "selector", "timeout_ms")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		selector, err := rpcString(merged, "selector")
		if err != nil {
			return nil, err
		}
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		found, err := b.waitForSelector(ctx, conn, tabID, selector, rpcMilliseconds(merged, "timeout_ms", 5000))
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"ok": false, "error": "Element not found within timeout: " + selector}, nil
		}
		return map[string]any{"ok": true, "selector": selector}, nil
	},
	"wait_for_text": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "text", "timeout_ms")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		text, err := rpcString(merged, "text")
		if err != nil {
			return nil, err
		}
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		found, err := b.waitForText(ctx, conn, tabID, text, rpcMilliseconds(merged, "timeout_ms", 5000))
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"ok": false, "error": "Text not found within timeout: " + text}, nil
		}
		return map[string]any{"ok": true, "text": text}, nil
	},
	"resize": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdBrowserResize(ctx, b, "", rpcParams(args, params, "tab_id", "width", "height"))
	},
	"handle_javascript_dialog": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "accept", "prompt_text")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		accept, ok := merged["accept"].(bool)
		if !ok {
			return nil, badRequest("'accept' must be a boolean")
		}
		action := "dismiss"
		if accept {
			action = "accept"
		}
		return cmdBrowserDialogRespond(ctx, b, "", map[string]any{
			"tab_id": tabID, "action": action, "prompt_text": merged["prompt_text"],
		})
	},
	"get_pending_dialog": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdGetPendingDialog(ctx, b, "", map[string]any{"tab_id": tabID})
	},

	// ── tab health / action history ─────────────────────────────────────
	"get_tab_blockers": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdGetTabBlockers(ctx, b, "", map[string]any{"tab_id": tabID})
	},
	"tab_health": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdTabHealth(ctx, b, "", map[string]any{"tab_id": tabID})
	},
	"audit_tab": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return cmdAuditTab(ctx, b, "", map[string]any{"tab_id": tabID})
	},
	"record_action": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		return cmdRecordAction(ctx, b, "", rpcParams(args, params, "tab_id", "verb", "target", "ok", "ts_ms"))
	},

	// ── cdp ─────────────────────────────────────────────────────────────
	"cdp_attach": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		fresh, err := conn.ensureAttached(ctx, tabID)
		if err != nil {
			return nil, err
		}
		conn.markAttached(tabID)
		if fresh {
			b.auditOnAttach(ctx, conn, tabID)
		}
		return map[string]any{"ok": true, "attached": true, "already_attached": !fresh, "tabId": tabID}, nil
	},
	"cdp_detach": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		return b.CDPDetach(ctx, tabID)
	},
	"_cdp": func(ctx context.Context, b *Bridge, args []any, params map[string]any) (any, error) {
		merged := rpcParams(args, params, "tab_id", "method", "params")
		tabID, err := rpcTabID(args, params)
		if err != nil {
			return nil, err
		}
		method, err := rpcString(merged, "method")
		if err != nil {
			return nil, err
		}
		cdpParams, _ := merged["params"].(map[string]any)
		conn, err := rpcConnection(ctx, b, tabID)
		if err != nil {
			return nil, err
		}
		return b.cdp(ctx, conn, tabID, method, cdpParams, b.cfg.CommandTimeout)
	},

	// ── introspection ──────────────────────────────────────────────────
	"status_payload": func(ctx context.Context, b *Bridge, _ []any, _ map[string]any) (any, error) {
		return b.Status(ctx), nil
	},
	"connected_profiles": func(_ context.Context, b *Bridge, _ []any, _ map[string]any) (any, error) {
		return b.connectedProfiles(), nil
	},
	"connection_help": func(_ context.Context, _ *Bridge, _ []any, _ map[string]any) (any, error) {
		return connectionHelp(), nil
	},
	"is_connected": func(_ context.Context, b *Bridge, _ []any, _ map[string]any) (any, error) {
		return b.IsConnected(), nil
	},
}

// dispatchRPC runs one allowlisted method. The caller has already checked the
// allowlist and handled the two per-connection methods (client_hello,
// subscribe_notifications).
func dispatchRPC(ctx context.Context, b *Bridge, method string, args []any, params map[string]any) (any, error) {
	handler, ok := rpcDispatchTable[method]
	if !ok {
		// Every allowlisted name has an adapter; this only fires if the two
		// tables drift apart, which the package test pins.
		return nil, newError("unknown_method",
			fmt.Sprintf("Bridge RPC method %q is allowlisted but has no dispatcher", method), false)
	}
	return handler(ctx, b, args, params)
}

// missingRPCAdapters is used by tests to prove the allowlist and the dispatch
// table cannot drift.
func missingRPCAdapters() []string {
	missing := make([]string, 0)
	for name := range rpcMethods {
		if name == "client_hello" || name == "subscribe_notifications" {
			continue
		}
		if _, ok := rpcDispatchTable[name]; !ok {
			missing = append(missing, name)
		}
	}
	sortStrings(missing)
	return missing
}
