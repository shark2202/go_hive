package browserbridge

// commands_page_contract_test.go pins the D08 return contract this port adds:
// screenshot JPEG/800px/annotation metadata and snapshot refs/href/DOM
// fallback/dialog gating. Cases are Go-side because upstream's equivalents live
// in the Python tool layer (inspection.py / refs.py) with no pytest coverage of
// the wire shape.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
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
				// Chrome returns device pixels: clip.width × scale × dpr. This
				// fake page is dpr 2 (measured on real Chrome: an 800-CSS-px
				// clip at dpr 2 comes back 1600 px wide).
				width, height := 1600*2, 1000*2
				if clip, ok := inner["clip"].(map[string]any); ok {
					scale := paramFloat(clip, "scale", 1)
					width = int(paramFloat(clip, "width", 1600) * scale * 2)
					height = int(paramFloat(clip, "height", 1000) * scale * 2)
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
	// clip.scale doubles as the CSS→device divisor: Chrome returns
	// width × scale × dpr device pixels, so an 800px IMAGE needs
	// 800/(1600×2) here (the fake page is 1600 CSS px at dpr 2).
	if !ok || paramFloat(clip, "scale", 0) != 0.25 {
		t.Fatalf("capture clip=%+v", capture["clip"])
	}
	if capture["format"] != "jpeg" || toFloatMust(t, capture["quality"]) != 75 {
		t.Fatalf("capture params=%+v", capture)
	}
}

// dprCDP is the contractCDP fake re-parameterised to (dpr, cssW, cssH) and
// modelling Chrome's capture bitmap size: clip.width × scale × dpr device px.
func dprCDP(t *testing.T, dpr, cssW, cssH float64) func(fakeCommand) (any, string) {
	t.Helper()
	base := contractCDP(t)
	return func(cmd fakeCommand) (any, string) {
		if cmd.Type == "cdp" {
			method, _ := cmd.Params["method"].(string)
			inner, _ := cmd.Params["params"].(map[string]any)
			switch method {
			case "Runtime.evaluate":
				expr, _ := inner["expression"].(string)
				if strings.Contains(expr, "devicePixelRatio") {
					return map[string]any{"result": map[string]any{"value": map[string]any{
						"url": "https://example.com/", "dpr": dpr, "cssWidth": cssW, "cssHeight": cssH,
					}}}, ""
				}
			case "Page.captureScreenshot":
				format, _ := inner["format"].(string)
				if format == "" {
					format = "png"
				}
				width, height := int(cssW*dpr), int(cssH*dpr)
				if clip, ok := inner["clip"].(map[string]any); ok {
					scale := paramFloat(clip, "scale", 1)
					width = int(paramFloat(clip, "width", cssW) * scale * dpr)
					height = int(paramFloat(clip, "height", cssH) * scale * dpr)
				}
				return map[string]any{"data": encodeTestImage(t, format, width, height)}, ""
			}
		}
		return base(cmd)
	}
}

// TestScreenshotMaxWidthIsDprIndependent pins the dpr compensation on the
// max_width path: clip.scale is in CSS px while Chrome's bitmap is in device
// px, so the requested scale is max_width/(cssWidth×dpr) and the returned
// image is max_width px wide on every dpr — the same 800px-wide image
// upstream's PIL resize always produces. Without it, a dpr=2 host returned a
// 1600px-wide annotated screenshot (the registered dpr>1 exception).
//
// Parity: tools/src/gcu/browser/tools/inspection.py::_resize_and_annotate
func TestScreenshotMaxWidthIsDprIndependent(t *testing.T) {
	const cssW, cssH = 1280.0, 720.0
	for _, dpr := range []float64{1, 2, 3} {
		t.Run(fmt.Sprintf("dpr=%g", dpr), func(t *testing.T) {
			b := newTestBridge(t, Config{CommandTimeout: 5 * time.Second, ArtifactDir: t.TempDir()})
			f := startFakeExtension(t, b.WSAddr(), helloFor("alpha"), dprCDP(t, dpr, cssW, cssH))
			waitFor(t, 2*time.Second, func() bool {
				_, err := b.resolve("alpha", nil)
				return err == nil
			}, "hello")
			res := execTool(t, b, "browser_screenshot", map[string]any{
				"tab_id": 11, "format": "jpeg", "quality": 75, "max_width": 800,
			})
			if res["imageWidth"] != 800 || res["imageHeight"] != 450 {
				t.Fatalf("dpr=%g image=%vx%v, want 800x450", dpr, res["imageWidth"], res["imageHeight"])
			}
			if w, h := imageDimensions(decodeBase64Image(stringOf(res["data"]))); w != 800 || h != 450 {
				t.Fatalf("dpr=%g decoded jpeg=%dx%d, want 800x450", dpr, w, h)
			}
			capture := lastCDPParams(f, "Page.captureScreenshot")
			clip, _ := capture["clip"].(map[string]any)
			want := 800.0 / (cssW * dpr)
			if got := paramFloat(clip, "scale", 0); math.Abs(got-want) > 1e-9 {
				t.Fatalf("dpr=%g clip.scale=%v, want %v", dpr, got, want)
			}
			if got := toIntMust(t, res["capturedWidth"]); got != int(cssW*dpr) {
				t.Fatalf("dpr=%g capturedWidth=%d, want %d", dpr, got, int(cssW*dpr))
			}
			if got := toFloatMust(t, res["physicalScale"]); got != round4(cssW*dpr/800) {
				t.Fatalf("dpr=%g physicalScale=%v, want %v", dpr, got, round4(cssW*dpr/800))
			}
		})
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

// TestInteractCoordinateClickAttachesAnnotatedImage pins R15: a coordinate click
// returns its interaction dict plus one annotated viewport screenshot, captured
// through the shared browser_screenshot path so the just-recorded highlight is
// drawn (upstream `_build_visual_response`).
//
// Parity: tools/src/gcu/browser/tools/interact.py::_build_visual_response
func TestInteractCoordinateClickAttachesAnnotatedImage(t *testing.T) {
	b, f := newContractBridge(t)
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "left_click", "coordinate": []any{0.5, 0.5}, "tab_id": 11,
	})
	if res["ok"] != true || res["action"] != "click_coordinate" {
		t.Fatalf("action result=%+v", res)
	}
	if res["mimeType"] != "image/jpeg" {
		t.Fatalf("mimeType=%v", res["mimeType"])
	}
	raw := decodeBase64Image(stringOf(res["data"]))
	if w, h := imageDimensions(raw); w != 800 || h != 500 {
		t.Fatalf("attached jpeg = %dx%d, want 800x500", w, h)
	}
	// Only the bridge-native image carrier is added — the action dict must not
	// grow the screenshot metadata keys.
	for _, key := range []string{"imageWidth", "imageHeight", "cssWidth", "cssHeight", "capturedWidth", "capturedHeight", "physicalScale", "devicePixelRatio", "annotated", "format", "saved_to", "crop_box", "scaleHint", "clip", "fullPage"} {
		if _, present := res[key]; present {
			t.Fatalf("action result gained screenshot metadata key %q: %+v", key, res)
		}
	}
	// The capture annotates the click that just happened: overlay inject →
	// capture → overlay removal, with the click point in the label.
	inject, capture, remove := -1, -1, -1
	var injectExpr string
	for i, cmd := range f.commands() {
		if cmd.Type != "cdp" {
			continue
		}
		method, _ := cmd.Params["method"].(string)
		inner, _ := cmd.Params["params"].(map[string]any)
		switch method {
		case "Page.captureScreenshot":
			capture = i
		case "Runtime.evaluate":
			expr, _ := inner["expression"].(string)
			switch {
			case strings.Contains(expr, "__hive_shot_hl") && strings.Contains(expr, "click (800.0,500.0)"):
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
	if !strings.Contains(injectExpr, "(400,250) click (800.0,500.0)") {
		t.Fatalf("overlay label missing: %s", injectExpr)
	}
}

// TestInteractCoordinateHoverAndKeyAttachAnnotatedImage pins R15 for the other
// two coordinate actions: hover and key carry the same annotated screenshot,
// labelled with the point they landed on.
//
// Parity: tools/src/gcu/browser/tools/interact.py::_build_visual_response
func TestInteractCoordinateHoverAndKeyAttachAnnotatedImage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		params map[string]any
		action string
		label  string
	}{
		{"hover", map[string]any{"action": "hover", "coordinate": []any{0.25, 0.5}, "tab_id": 11}, "hover_coordinate", "hover (400.0,500.0)"},
		{"key", map[string]any{"action": "key", "text": "Enter", "coordinate": []any{0.5, 0.5}, "tab_id": 11}, "press_at", "Enter (800.0,500.0)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, f := newContractBridge(t)
			res := execTool(t, b, "browser_interact", tc.params)
			if res["ok"] != true || res["action"] != tc.action {
				t.Fatalf("action result=%+v", res)
			}
			raw := decodeBase64Image(stringOf(res["data"]))
			if w, h := imageDimensions(raw); w != 800 || h != 500 {
				t.Fatalf("attached jpeg = %dx%d, want 800x500", w, h)
			}
			found := false
			for _, cmd := range f.commands() {
				if cmd.Type != "cdp" || cmd.Params["method"] != "Runtime.evaluate" {
					continue
				}
				inner, _ := cmd.Params["params"].(map[string]any)
				expr, _ := inner["expression"].(string)
				if strings.Contains(expr, "__hive_shot_hl") && strings.Contains(expr, tc.label) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("annotated overlay never carried %q", tc.label)
			}
		})
	}
}

// TestInteractCoordinateImageFailureDegradesToActionResult pins upstream's
// text-only fallback: a screenshot failure never fails the interaction and
// never attaches an image.
//
// Parity: tools/src/gcu/browser/tools/interact.py::_build_visual_response
func TestInteractCoordinateImageFailureDegradesToActionResult(t *testing.T) {
	b := newTestBridge(t, Config{CommandTimeout: 5 * time.Second, ArtifactDir: t.TempDir()})
	inner := contractCDP(t)
	startFakeExtension(t, b.WSAddr(), helloFor("alpha"), func(cmd fakeCommand) (any, string) {
		if cmd.Type == "cdp" && cmd.Params["method"] == "Page.captureScreenshot" {
			return nil, "capture failed"
		}
		return inner(cmd)
	})
	waitFor(t, 2*time.Second, func() bool {
		_, err := b.resolve("alpha", nil)
		return err == nil
	}, "hello")
	res := execTool(t, b, "browser_interact", map[string]any{
		"action": "left_click", "coordinate": []any{0.5, 0.5}, "tab_id": 11,
	})
	if res["ok"] != true || res["action"] != "click_coordinate" {
		t.Fatalf("interaction must survive a capture failure: %+v", res)
	}
	if _, present := res["data"]; present {
		t.Fatalf("capture failure must not attach an image: %+v", res)
	}
	if toFloatMust(t, res["x"]) != 800 || toFloatMust(t, res["y"]) != 500 {
		t.Fatalf("click point = %v,%v", res["x"], res["y"])
	}
}
