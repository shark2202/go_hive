package browserbridge

// commands_page_contract_test.go pins the D08 return contract this port adds:
// screenshot JPEG/800px/annotation metadata and snapshot refs/href/DOM
// fallback/dialog gating. Cases are Go-side because upstream's equivalents live
// in the Python tool layer (inspection.py / refs.py) with no pytest coverage of
// the wire shape.

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strconv"
	"strings"
	"testing"
	"time"
)

// lastCDPParams returns the params of the last call to one CDP method.
func lastCDPParams(f *fakeExtension, method string) map[string]any {
	var out map[string]any
	for _, cmd := range f.commands() {
		if cmd.Type != "cdp" || cmd.Params["method"] != method {
			continue
		}
		inner, _ := cmd.Params["params"].(map[string]any)
		out = inner
	}
	return out
}

func encodeTestImage(t *testing.T, format string, width, height int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width; x++ {
		for y := 0; y < height; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 255), G: uint8(y % 255), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	var err error
	if format == "jpeg" {
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 75})
	} else {
		err = png.Encode(&buf, img)
	}
	if err != nil {
		t.Fatalf("encode fixture image: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// contractCDP serves one fake page: 1600x1000 CSS viewport at dpr 2, one link
// with an href visible in the DOM, and an AX tree with a button + that link.
func contractCDP(t *testing.T) func(fakeCommand) (any, string) {
	return func(cmd fakeCommand) (any, string) {
		switch cmd.Type {
		case "cdp.attach":
			return map[string]any{"ok": true, "attached": true}, ""
		case "cdp.detach":
			return map[string]any{"ok": true}, ""
		case "cdp":
			method, _ := cmd.Params["method"].(string)
			inner, _ := cmd.Params["params"].(map[string]any)
			switch method {
			case "Page.enable", "Accessibility.enable", "Runtime.enable", "DOM.enable":
				return map[string]any{}, ""
			case "Page.getLayoutMetrics":
				return map[string]any{"contentSize": map[string]any{"width": 1600, "height": 3000}}, ""
			case "Page.captureScreenshot":
				format, _ := inner["format"].(string)
				if format == "" {
					format = "png"
				}
				width, height := 1600, 1000
				if clip, ok := inner["clip"].(map[string]any); ok {
					scale := paramFloat(clip, "scale", 1)
					width = int(paramFloat(clip, "width", 1600) * scale)
					height = int(paramFloat(clip, "height", 1000) * scale)
				}
				return map[string]any{"data": encodeTestImage(t, format, width, height)}, ""
			case "Runtime.evaluate":
				expr, _ := inner["expression"].(string)
				switch {
				case strings.Contains(expr, "devicePixelRatio"):
					return map[string]any{"result": map[string]any{"value": map[string]any{
						"url": "https://example.com/", "dpr": 2.0, "cssWidth": 1600.0, "cssHeight": 1000.0,
					}}}, ""
				case strings.Contains(expr, "querySelectorAll('a[href]')"):
					return map[string]any{"result": map[string]any{"value": []any{
						[]any{"Docs", "https://example.com/docs"},
						[]any{"Orphan", "https://example.com/orphan"},
					}}}, ""
				case strings.Contains(expr, "interactiveSelectors"):
					return map[string]any{"result": map[string]any{"value": []any{
						map[string]any{"tag": "a", "role": "link", "text": "Home", "href": "https://example.com/"},
					}}}, ""
				case strings.Contains(expr, "__hive_shot_hl"):
					return map[string]any{"result": map[string]any{"value": "ok"}}, ""
				case strings.Contains(expr, "window.location.href"):
					return map[string]any{"result": map[string]any{"value": "https://example.com/"}}, ""
				case strings.Contains(expr, "getBoundingClientRect"):
					return map[string]any{"result": map[string]any{"value": map[string]any{
						"left": 100.0, "top": 50.0, "x": 120.0, "y": 60.0, "w": 40.0, "h": 20.0,
					}}}, ""
				case strings.Contains(expr, "innerWidth"):
					return map[string]any{"result": map[string]any{"value": map[string]any{"w": 1600.0, "h": 1000.0}}}, ""
				}
				return map[string]any{"result": map[string]any{"value": nil}}, ""
			case "Accessibility.getFullAXTree":
				return map[string]any{"nodes": []any{
					map[string]any{"nodeId": "1", "ignored": false, "role": map[string]any{"value": "RootWebArea"},
						"name": map[string]any{"value": "Doc"}, "childIds": []any{"2", "3"}},
					map[string]any{"nodeId": "2", "ignored": false, "role": map[string]any{"value": "button"},
						"name": map[string]any{"value": "Go"}, "childIds": []any{}},
					map[string]any{"nodeId": "3", "ignored": false, "role": map[string]any{"value": "link"},
						"name": map[string]any{"value": "Docs"}, "childIds": []any{}},
				}}, ""
			case "Input.dispatchMouseEvent", "Input.insertText", "Input.dispatchKeyEvent":
				return map[string]any{}, ""
			}
			return map[string]any{}, ""
		case "tab.list":
			return map[string]any{"tabs": []any{map[string]any{"id": 11, "url": "https://example.com/", "title": "Example", "groupId": -1}}}, ""
		}
		return map[string]any{"ok": true}, ""
	}
}

func newContractBridge(t *testing.T) (*Bridge, *fakeExtension) {
	t.Helper()
	b := newTestBridge(t, Config{CommandTimeout: 5 * time.Second, ArtifactDir: t.TempDir()})
	f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), contractCDP(t))
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	return b, f
}

// TestScreenshotMaxWidthJpegsAt800 pins the bridge half of R01: max_width makes
// the bridge ask Chrome for an 800px-wide JPEG (clip scale), and the result
// carries the upstream metadata the CLI reshapes.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::_resize_and_annotate
func TestScreenshotMaxWidthJpegsAt800(t *testing.T) {
	b, f := newContractBridge(t)
	res := execTool(t, b, "browser_screenshot", map[string]any{
		"tab_id": 11, "format": "jpeg", "quality": 75, "max_width": 800,
	})
	if res["ok"] != true || res["format"] != "jpeg" || res["mimeType"] != "image/jpeg" {
		t.Fatalf("result=%+v", res)
	}
	if res["imageWidth"] != 800 || res["imageHeight"] != 500 {
		t.Fatalf("image=%vx%v", res["imageWidth"], res["imageHeight"])
	}
	raw := decodeBase64Image(stringOf(res["data"]))
	if w, h := imageDimensions(raw); w != 800 || h != 500 {
		t.Fatalf("decoded jpeg = %dx%d", w, h)
	}
	if res["cssWidth"] != 1600.0 || res["cssHeight"] != 1000.0 || res["devicePixelRatio"] != 2.0 {
		t.Fatalf("meta=%+v", res)
	}
	if res["capturedWidth"] != 3200 || res["capturedHeight"] != 2000 {
		t.Fatalf("captured=%vx%v", res["capturedWidth"], res["capturedHeight"])
	}
	if res["physicalScale"] != 4.0 {
		t.Fatalf("physicalScale=%v, want 4 (1600*2/800)", res["physicalScale"])
	}
	if res["annotated"] != false {
		t.Fatalf("annotated=%v without a tracked interaction", res["annotated"])
	}
	capture := lastCDPParams(f, "Page.captureScreenshot")
	clip, ok := capture["clip"].(map[string]any)
	if !ok || paramFloat(clip, "scale", 0) != 0.5 {
		t.Fatalf("capture clip=%+v", capture["clip"])
	}
	if capture["format"] != "jpeg" || toFloatMust(t, capture["quality"]) != 75 {
		t.Fatalf("capture params=%+v", capture)
	}
}

// TestScreenshotAnnotatesTrackedHighlight pins the annotation half of R01: a
// selector click records the last-interaction rect, and the next screenshot
// injects the overlay (labelled with the selector), captures it, then removes
// it.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::_resize_and_annotate
func TestScreenshotAnnotatesTrackedHighlight(t *testing.T) {
	b, f := newContractBridge(t)
	execTool(t, b, "browser_click", map[string]any{"tab_id": 11, "selector": "#go"})
	if b.tabHighlight(11) == nil {
		t.Fatal("click did not record a highlight")
	}
	res := execTool(t, b, "browser_screenshot", map[string]any{
		"tab_id": 11, "format": "jpeg", "quality": 75, "max_width": 800, "annotate": true,
	})
	if res["annotated"] != true {
		t.Fatalf("annotated=%v, want true: %+v", res["annotated"], res)
	}
	var inject, remove, capture = -1, -1, -1
	var injectExpr string
	commands := f.commands()
	for i, cmd := range commands {
		if cmd.Type != "cdp" {
			continue
		}
		method, _ := cmd.Params["method"].(string)
		inner, _ := cmd.Params["params"].(map[string]any)
		expr, _ := inner["expression"].(string)
		switch method {
		case "Page.captureScreenshot":
			capture = i
		case "Runtime.evaluate":
			switch {
			case strings.Contains(expr, "__hive_shot_hl") && strings.Contains(expr, "#go"):
				inject = i
				injectExpr = expr
			case strings.Contains(expr, "__hive_shot_hl") && strings.Contains(expr, "remove"):
				remove = i
			}
		}
	}
	if inject < 0 || capture < 0 || remove < 0 {
		t.Fatalf("inject=%d capture=%d remove=%d", inject, capture, remove)
	}
	if !(inject < capture && capture < remove) {
		t.Fatalf("overlay must bracket the capture: inject=%d capture=%d remove=%d", inject, capture, remove)
	}
	// The overlay mirrors upstream's PIL annotation: blue box + "(x,y) label".
	for _, want := range []string{`"(50,25) #go"`, "rgba(59,130,246", "border:2px solid"} {
		if !strings.Contains(injectExpr, want) {
			t.Fatalf("overlay js missing %q: %s", want, injectExpr)
		}
	}
	if strings.Contains(injectExpr, "%!") {
		t.Fatalf("overlay js has a fmt formatting error: %s", injectExpr)
	}
}

// TestSnapshotAddsRefsHrefAndMissedLinks pins R02's tree shape: eN refs on
// every interactive/named node, inline [href="…"] on link nodes, and the
// supplemental section for DOM-visible links the AX walk never reached.
//
// Parity: tools/src/gcu/browser/bridge.py::_format_ax_tree
func TestSnapshotAddsRefsHrefAndMissedLinks(t *testing.T) {
	b, _ := newContractBridge(t)
	res := execTool(t, b, "browser_snapshot", map[string]any{"tab_id": 11})
	if res["ok"] != true {
		t.Fatalf("result=%+v", res)
	}
	tree := stringOf(res["tree"])
	for _, want := range []string{
		`- RootWebArea [ref=e1] "Doc"`,
		`- button [ref=e2] "Go"`,
		`- link [ref=e3] "Docs" [href="https://example.com/docs"]`,
		"(links visible in DOM but not in accessibility tree):",
		`- link [ref=e4] "Orphan" [href="https://example.com/orphan"]`,
	} {
		if !strings.Contains(tree, want) {
			t.Fatalf("tree missing %q:\n%s", want, tree)
		}
	}
	if res["nodes"] != 3 {
		t.Fatalf("nodes=%v", res["nodes"])
	}
	if res["url"] != "https://example.com/" {
		t.Fatalf("url=%v", res["url"])
	}
}

// TestSnapshotFallsBackToDOMWhenAXMostlyIgnored pins R02's DOM fallback: an AX
// tree with >500 nodes but <10 visible ones uses the DOM scan (e0-based refs,
// [href] marker), exactly like upstream.
//
// Parity: tools/src/gcu/browser/bridge.py::_dom_snapshot
func TestSnapshotFallsBackToDOMWhenAXMostlyIgnored(t *testing.T) {
	handler := func(cmd fakeCommand) (any, string) {
		if cmd.Type == "cdp" {
			method, _ := cmd.Params["method"].(string)
			inner, _ := cmd.Params["params"].(map[string]any)
			switch method {
			case "Accessibility.getFullAXTree":
				nodes := []any{map[string]any{"nodeId": "1", "ignored": false,
					"role": map[string]any{"value": "RootWebArea"}, "name": map[string]any{"value": "Doc"}}}
				for i := 2; i <= 501; i++ {
					nodes = append(nodes, map[string]any{
						"nodeId": strconv.Itoa(i), "ignored": true,
						"role": map[string]any{"value": "generic"}, "name": map[string]any{"value": ""},
					})
				}
				return map[string]any{"nodes": nodes}, ""
			case "Runtime.evaluate":
				expr, _ := inner["expression"].(string)
				if strings.Contains(expr, "interactiveSelectors") {
					return map[string]any{"result": map[string]any{"value": []any{
						map[string]any{"tag": "a", "role": "link", "text": "Home", "href": "https://example.com/"},
					}}}, ""
				}
				return map[string]any{"result": map[string]any{"value": "https://example.com/"}}, ""
			}
			return map[string]any{}, ""
		}
		return contractCDP(t)(cmd)
	}
	b := newTestBridge(t, Config{CommandTimeout: 5 * time.Second, ArtifactDir: t.TempDir()})
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), handler)
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	res := execTool(t, b, "browser_snapshot", map[string]any{"tab_id": 11})
	tree := stringOf(res["tree"])
	if !strings.Contains(tree, `- link "Home" [href] [ref=e0]`) {
		t.Fatalf("DOM fallback tree=%q", tree)
	}
}

// TestSnapshotBlockedByPendingDialog pins R02's dialog gate: a tracked native
// dialog short-circuits the snapshot with the upstream error shape instead of
// blocking on the AX call.
//
// Parity: tools/src/gcu/browser/tools/inspection.py::browser_snapshot
func TestSnapshotBlockedByPendingDialog(t *testing.T) {
	b, _ := newContractBridge(t)
	b.setPendingDialog(11, map[string]any{
		"type": "confirm", "message": "Sure?", "default_prompt": "", "url": "https://example.com/", "opened_at_ms": 1,
	})
	res := execTool(t, b, "browser_snapshot", map[string]any{"tab_id": 11})
	if res["ok"] != false || res["error"] != "Page is blocked by a native browser dialog" {
		t.Fatalf("result=%+v", res)
	}
	dialog, ok := res["pending_dialog"].(map[string]any)
	if !ok || dialog["type"] != "confirm" || dialog["message"] != "Sure?" || toIntMust(t, dialog["tab_id"]) != 11 {
		t.Fatalf("pending_dialog=%+v", res["pending_dialog"])
	}
}

func toFloatMust(t *testing.T, v any) float64 {
	t.Helper()
	f, ok := toFloat(v)
	if !ok {
		t.Fatalf("not a number: %T %v", v, v)
	}
	return f
}
