package browserbridge

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// commands_view.go ports the viewport/input-adjacent advanced tools:
// browser_resize, browser_upload and browser_dialog_respond.

// cmdBrowserResize ports bridge.resize: a CDP device-metrics override (the
// Chrome window itself is not resized).
func cmdBrowserResize(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	width := paramInt(params, "width", 0)
	height := paramInt(params, "height", 0)
	if width <= 0 || height <= 0 {
		return nil, badRequest("'width' and 'height' must be positive integers")
	}
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	if _, err := b.cdp(ctx, conn, tabID, "Emulation.setDeviceMetricsOverride", map[string]any{
		"width": width, "height": height, "deviceScaleFactor": 0, "mobile": false,
	}, 15*time.Second); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "action": "resize", "tabId": tabID, "width": width, "height": height}, nil
}

// cmdBrowserUpload ports tools/advanced.py browser_upload.
//
// Two modes: direct (the file input already exists) and triggered (click a
// trigger that creates the input and opens the native picker — the picker is
// intercepted via Page.setInterceptFileChooserDialog so it never blocks).
func cmdBrowserUpload(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	selector, _ := paramString(params, "selector")
	if strings.TrimSpace(selector) == "" {
		return nil, badRequest("missing required parameter 'selector'")
	}
	files, err := paramStringList(params, "file_paths")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, badRequest("missing required parameter 'file_paths'")
	}
	triggerSelector, hasTrigger := paramString(params, "trigger_selector")
	if hasTrigger && strings.TrimSpace(triggerSelector) == "" {
		hasTrigger = false
	}
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}

	for _, path := range files {
		if _, statErr := os.Stat(path); statErr != nil {
			return map[string]any{"ok": false, "error": "File not found: " + path}, nil
		}
	}
	timeout := time.Duration(paramIntAny(params, 30000, "timeout_ms", "timeoutMs")) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	if _, err := b.cdp(ctx, conn, tabID, "DOM.enable", nil, 10*time.Second); err != nil {
		return nil, err
	}

	interceptEnabled := false
	defer func() {
		if interceptEnabled {
			// Best-effort: a failed disable must not mask the upload result.
			_, _ = b.cdp(context.WithoutCancel(ctx), conn, tabID, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": false}, 3*time.Second)
		}
	}()

	if hasTrigger {
		if _, err := b.cdp(ctx, conn, tabID, "Page.enable", nil, 10*time.Second); err != nil {
			return nil, err
		}
		if _, err := b.cdp(ctx, conn, tabID, "Page.setInterceptFileChooserDialog", map[string]any{"enabled": true}, 10*time.Second); err != nil {
			return nil, err
		}
		interceptEnabled = true
		// A bare element.click() does not open the picker on pointer-driven
		// menus (Drive, many SPAs); dispatch the full pointer/mouse sequence.
		clickJS := fmt.Sprintf(`(function(){
var el = document.querySelector(%s);
if (!el) return 'no-trigger';
var r = el.getBoundingClientRect();
var cx = r.left + r.width / 2, cy = r.top + r.height / 2;
['pointerdown','mousedown','pointerup','mouseup','click'].forEach(function(t){
  var E = t.indexOf('pointer') === 0 ? PointerEvent : MouseEvent;
  el.dispatchEvent(new E(t, {bubbles: true, cancelable: true, composed: true, clientX: cx, clientY: cy, button: 0, view: window}));
});
return 'clicked';
})()`, jsString(triggerSelector))
		v, err := b.evalJS(ctx, conn, tabID, clickJS, 15*time.Second)
		if err != nil {
			return nil, err
		}
		if res, _ := v.(string); res == "no-trigger" {
			return map[string]any{"ok": false, "error": "Trigger element not found: " + triggerSelector}, nil
		}
	}

	deadline := time.Now().Add(timeout)
	nodeID := 0
	for {
		// Re-fetch the document each poll: triggered inputs are appended after
		// the first getDocument, and a stale root nodeId misses them.
		doc, err := b.cdp(ctx, conn, tabID, "DOM.getDocument", map[string]any{"depth": 0}, 10*time.Second)
		if err != nil {
			return nil, err
		}
		root := 0
		if rootMap, ok := doc["root"].(map[string]any); ok {
			root, _ = toInt(rootMap["nodeId"])
		}
		if root > 0 {
			res, err := b.cdp(ctx, conn, tabID, "DOM.querySelector", map[string]any{
				"nodeId": root, "selector": selector,
			}, 10*time.Second)
			if err != nil {
				return nil, err
			}
			if id, ok := toInt(res["nodeId"]); ok && id > 0 {
				nodeID = id
				break
			}
		}
		if time.Now().After(deadline) {
			return map[string]any{"ok": false, "error": "Element not found: " + selector}, nil
		}
		select {
		case <-ctx.Done():
			return nil, newError(CodeTimeout, "upload cancelled", false)
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Track whether the page's change handler ran. Sites commonly clear the
	// input after consuming the files, so `input.files` afterwards is not a
	// reliable acceptance signal — a one-shot change listener installed before
	// setFileInputFiles is.
	objectID := ""
	if resolved, err := b.cdp(ctx, conn, tabID, "DOM.resolveNode", map[string]any{"nodeId": nodeID}, 10*time.Second); err == nil {
		if obj, ok := resolved["object"].(map[string]any); ok {
			objectID = stringOf(obj["objectId"])
		}
	}
	if objectID != "" {
		_, _ = b.cdp(ctx, conn, tabID, "Runtime.callFunctionOn", map[string]any{
			"objectId": objectID,
			"functionDeclaration": "function(){this.__hiveChangeFired=false;" +
				"this.addEventListener('change',function(){this.__hiveChangeFired=true;},{once:true});}",
		}, 10*time.Second)
	}

	if _, err := b.cdp(ctx, conn, tabID, "DOM.setFileInputFiles", map[string]any{
		"files": files, "nodeId": nodeID,
	}, 30*time.Second); err != nil {
		return nil, err
	}

	var accepted any
	if objectID != "" {
		if rb, err := b.cdp(ctx, conn, tabID, "Runtime.callFunctionOn", map[string]any{
			"objectId":      objectID,
			"returnByValue": true,
			"functionDeclaration": "function(){return {change_fired:!!this.__hiveChangeFired," +
				"files:Array.prototype.slice.call(this.files).map(function(f){return {name:f.name,size:f.size,type:f.type};})};}",
		}, 10*time.Second); err == nil {
			if result, ok := rb["result"].(map[string]any); ok {
				accepted = result["value"]
			}
		}
	}

	return map[string]any{
		"ok":       true,
		"action":   "upload",
		"selector": selector,
		"files":    files,
		"count":    len(files),
		"accepted": accepted,
	}, nil
}

// cmdBrowserDialogRespond ports bridge.handle_javascript_dialog as exposed by
// browser_dialog_respond.
//
// Deliberately no "is a dialog pending?" pre-check: this tool is the recovery
// path for a wedged tab where the opening event was missed. Chrome rejects the
// CDP call when nothing is open, which is translated into a clean result.
func cmdBrowserDialogRespond(ctx context.Context, b *Bridge, profile string, params map[string]any) (any, error) {
	action := strings.ToLower(strings.TrimSpace(mustString(params, "action")))
	if action != "accept" && action != "dismiss" {
		return nil, badRequest("'action' must be accept or dismiss")
	}
	tabID, err := b.resolveTabTarget(profile, params)
	if err != nil {
		return nil, err
	}
	conn, err := b.resolve(routeProfile(profile, params), &tabID)
	if err != nil {
		return nil, err
	}
	// A dialog pauses the renderer but not the extension; attach is safe here
	// because the extension's debugger session is independent of the dialog.
	if _, err := conn.ensureAttached(ctx, tabID); err != nil {
		return nil, err
	}
	cdpParams := map[string]any{"accept": action == "accept"}
	if promptText, ok := paramString(params, "prompt_text"); ok {
		cdpParams["promptText"] = promptText
	}
	if _, err := conn.Call(ctx, "cdp", map[string]any{
		"tabId": tabID, "method": "Page.handleJavaScriptDialog", "params": cdpParams,
	}, 5*time.Second); err != nil {
		msg := strings.ToLower(AsError(err).Message)
		if strings.Contains(msg, "no dialog") || strings.Contains(msg, "not showing") || strings.Contains(msg, "dialog is") {
			b.clearPendingDialog(tabID)
			return map[string]any{
				"ok": false, "tab_id": tabID,
				"error": "No native dialog is open on this tab",
			}, nil
		}
		return nil, err
	}
	dialog := b.dialogSnapshot(tabID)
	b.clearPendingDialog(tabID)
	return map[string]any{"ok": true, "tab_id": tabID, "dialog": dialog}, nil
}
