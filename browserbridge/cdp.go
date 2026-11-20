package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// cdp sends one Chrome DevTools Protocol command through the extension's
// generic `cdp` message. It attaches the debugger lazily and re-attaches once
// if Chrome reports the session was detached underneath us.
func (b *Bridge) cdp(ctx context.Context, conn *Connection, tabID int, method string, params map[string]any, timeout time.Duration) (map[string]any, error) {
	if params == nil {
		params = map[string]any{}
	}
	fresh, err := conn.ensureAttached(ctx, tabID)
	if err != nil {
		b.refreshBlockersFromError(tabID, AsError(err).Message)
		return nil, err
	}
	if fresh {
		b.auditOnAttach(ctx, conn, tabID)
	}
	if timeout <= 0 {
		timeout = b.cfg.CommandTimeout
	}
	raw, err := conn.Call(ctx, "cdp", map[string]any{
		"tabId":  tabID,
		"method": method,
		"params": params,
	}, timeout)
	if err != nil {
		if isNotAttached(err) {
			conn.forgetAttached(tabID)
			if _, attachErr := conn.ensureAttached(ctx, tabID); attachErr != nil {
				b.refreshBlockersFromError(tabID, AsError(attachErr).Message)
				return nil, attachErr
			}
			raw, err = conn.Call(ctx, "cdp", map[string]any{
				"tabId":  tabID,
				"method": method,
				"params": params,
			}, timeout)
		}
		if err != nil {
			e := AsError(err)
			b.refreshBlockersFromError(tabID, e.Message)
			return nil, newError(CodeCDPError,
				fmt.Sprintf("%s failed: %s", method, e.Message), e.Retryable)
		}
	}
	res, err := resultMap(raw)
	if err != nil {
		return nil, err
	}
	return res, nil
}

// auditOnAttach mirrors upstream's proactive tab.audit after a first attach:
// classify the fresh snapshot and cache any blockers. Failures are swallowed;
// the reactive CDP-error path catches what the audit missed.
func (b *Bridge) auditOnAttach(ctx context.Context, conn *Connection, tabID int) {
	_, _, _, protocol := conn.identity()
	if protocol < 2 {
		return
	}
	callCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := conn.Call(callCtx, "tab.audit", map[string]any{"tabId": tabID}, 2*time.Second)
	if err != nil {
		return
	}
	snap, err := resultMap(raw)
	if err != nil {
		return
	}
	b.refreshBlockers(tabID, snap, "", true)
}

// evalJS runs Runtime.evaluate with returnByValue and returns the JS value.
func (b *Bridge) evalJS(ctx context.Context, conn *Connection, tabID int, expression string, timeout time.Duration) (any, error) {
	res, err := b.cdp(ctx, conn, tabID, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  true,
	}, timeout)
	if err != nil {
		return nil, err
	}
	if ex, ok := res["exceptionDetails"]; ok && ex != nil {
		return nil, newError(CodeCDPError, exceptionMessage(ex), false)
	}
	inner, _ := res["result"].(map[string]any)
	if inner == nil {
		return nil, nil
	}
	value, ok := inner["value"]
	if !ok {
		return nil, nil
	}
	return value, nil
}

func exceptionMessage(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprint(v)
	}
	if ex, ok := m["exception"].(map[string]any); ok {
		if desc, ok := ex["description"].(string); ok && desc != "" {
			return desc
		}
	}
	if text, ok := m["text"].(string); ok && text != "" {
		return text
	}
	return "JavaScript evaluation failed"
}

type elementRect struct {
	Left   float64
	Top    float64
	X      float64
	Y      float64
	Width  float64
	Height float64
	Tag    string
}

func jsString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func (b *Bridge) elementRect(ctx context.Context, conn *Connection, tabID int, selector string) (elementRect, string, error) {
	expr := fmt.Sprintf(`(() => {
  const el = document.querySelector(%s);
  if (!el) return {error: "not_found"};
  el.scrollIntoView({block: "center", inline: "center"});
  const r = el.getBoundingClientRect();
  if (r.width === 0 || r.height === 0) return {error: "zero_dimensions"};
  return {left: r.left, top: r.top, x: r.left + r.width / 2, y: r.top + r.height / 2, w: r.width, h: r.height};
})()`, jsString(selector))

	v, err := b.evalJS(ctx, conn, tabID, expr, 10*time.Second)
	if err != nil {
		return elementRect{}, "", err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return elementRect{}, "not_found", nil
	}
	if msg, ok := m["error"].(string); ok {
		return elementRect{}, msg, nil
	}
	rect := elementRect{
		Left:   paramFloat(m, "left", 0),
		Top:    paramFloat(m, "top", 0),
		X:      paramFloat(m, "x", 0),
		Y:      paramFloat(m, "y", 0),
		Width:  paramFloat(m, "w", 0),
		Height: paramFloat(m, "h", 0),
	}
	return rect, "", nil
}

// pollElementRect waits for a selector to exist and be laid out.
func (b *Bridge) pollElementRect(ctx context.Context, conn *Connection, tabID int, selector string, timeout time.Duration) (elementRect, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	start := time.Now()
	var last string
	for {
		rect, problem, err := b.elementRect(ctx, conn, tabID, selector)
		if err != nil {
			return elementRect{}, err
		}
		if problem == "" {
			return rect, nil
		}
		last = problem
		if time.Since(start) >= timeout {
			if last == "zero_dimensions" {
				return elementRect{}, newError(CodeInvalidState,
					fmt.Sprintf("element %q has zero dimensions", selector), false)
			}
			return elementRect{}, newError(CodeNotFound,
				fmt.Sprintf("element not found: %s", selector), false)
		}
		elapsed := time.Since(start)
		sleep := 100 * time.Millisecond
		if elapsed < time.Second {
			sleep = 50 * time.Millisecond
		} else if elapsed > 5*time.Second {
			sleep = 300 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return elementRect{}, newError(CodeTimeout, "element lookup cancelled", false)
		case <-time.After(sleep):
		}
	}
}

func (b *Bridge) mouseClick(ctx context.Context, conn *Connection, tabID int, x, y float64, button string, clickCount int) error {
	if clickCount <= 0 {
		clickCount = 1
	}
	for _, typ := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": typ, "x": x, "y": y}
		if typ != "mouseMoved" {
			params["button"] = button
			params["clickCount"] = clickCount
		} else {
			params["button"] = "none"
		}
		if _, err := b.cdp(ctx, conn, tabID, "Input.dispatchMouseEvent", params, 10*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func normalizeButton(button string) (string, error) {
	switch button {
	case "", "left":
		return "left", nil
	case "right", "middle":
		return button, nil
	default:
		return "", badRequest("'button' must be left, right or middle")
	}
}
