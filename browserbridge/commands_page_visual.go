package browserbridge

// commands_page_visual.go holds the pixel-level helpers behind
// browser_screenshot's JPEG/annotation contract (upstream
// tools/inspection.py `_resize_and_annotate` + bridge.py highlight tracking).

import (
	"encoding/base64"
	"fmt"
)

// pyFloat renders a CSS-px coordinate the way upstream's Python f-string does:
// integral values keep one decimal ("422.0"), fractions use the shortest
// round-trip form. Labels are cosmetic, but matching them keeps screenshots
// comparable between the two implementations.
func pyFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%g", v)
}

// Round4 rounds to four decimals like upstream's round(x, 4).
// (round4 lives in commands_common.go.)

// imageDimensions reads the intrinsic size from a PNG (IHDR) or JPEG (SOF)
// header. Returns 0,0 for anything unrecognised; callers treat that as
// "unknown" rather than failing a successful capture.
func imageDimensions(raw []byte) (int, int) {
	if len(raw) >= 24 && raw[0] == 0x89 && raw[1] == 'P' && raw[2] == 'N' && raw[3] == 'G' {
		w := int(raw[16])<<24 | int(raw[17])<<16 | int(raw[18])<<8 | int(raw[19])
		h := int(raw[20])<<24 | int(raw[21])<<16 | int(raw[22])<<8 | int(raw[23])
		return w, h
	}
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return 0, 0
	}
	for i := 2; i+9 < len(raw); {
		if raw[i] != 0xFF {
			i++
			continue
		}
		marker := raw[i+1]
		// SOF0..SOF15, excluding DHT (0xC4), JPG (0xC8) and DAC (0xCC).
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			h := int(raw[i+5])<<8 | int(raw[i+6])
			w := int(raw[i+7])<<8 | int(raw[i+8])
			return w, h
		}
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		if i+3 >= len(raw) {
			break
		}
		length := int(raw[i+2])<<8 | int(raw[i+3])
		if length < 2 {
			break
		}
		i += 2 + length
	}
	return 0, 0
}

// decodeBase64Image unwraps a base64 capture payload; an empty/garbled value
// yields nil so callers can fall back to metadata-only behaviour.
func decodeBase64Image(data string) []byte {
	if data == "" {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil
	}
	return raw
}

// highlightOverlayJS builds a page-context overlay that reproduces the PIL
// annotation upstream draws on the resized screenshot: a blue box + label for
// selector actions, a red dot + crosshair for coordinate actions. Injecting it
// only for the capture keeps text rendering in the page's own font stack (no
// bitmap font tables), at the cost of a brief paint the user may notice.
func highlightOverlayJS(h map[string]any, cssWidth, outWidth float64) string {
	kind, _ := h["kind"].(string)
	label, _ := h["label"].(string)
	x := paramFloat(h, "x", 0)
	y := paramFloat(h, "y", 0)
	w := paramFloat(h, "w", 0)
	hh := paramFloat(h, "h", 0)
	cssToImage := 1.0
	if cssWidth > 0 && outWidth > 0 {
		cssToImage = cssWidth / outWidth
	}
	ix, iy := x/cssToImage, y/cssToImage
	displayLabel := fmt.Sprintf("(%d,%d) %s", int64(ix+0.5), int64(iy+0.5), label)

	if kind == "point" {
		r := 10.0
		return fmt.Sprintf(`(function(){
var old = document.getElementById('__hive_shot_hl');
if (old) old.remove();
var box = document.createElement('div');
box.id = '__hive_shot_hl';
box.style.cssText = 'position:fixed;left:' + %s + 'px;top:' + %s + 'px;width:0;height:0;z-index:2147483647;pointer-events:none;';
var dot = document.createElement('div');
dot.style.cssText = 'position:absolute;left:' + %s + 'px;top:' + %s + 'px;width:%s' + 'px;height:%s' + 'px;border-radius:50%%;background:rgba(239,68,68,0.39);border:2px solid rgba(239,68,68,0.86);box-sizing:border-box;';
var crossH = document.createElement('div');
crossH.style.cssText = 'position:absolute;left:' + %s + 'px;top:' + %s + 'px;width:%s' + 'px;height:2px;background:rgba(239,68,68,0.86);';
var crossV = document.createElement('div');
crossV.style.cssText = 'position:absolute;left:' + %s + 'px;top:' + %s + 'px;width:2px;height:%s' + 'px;background:rgba(239,68,68,0.86);';
var text = document.createElement('div');
text.textContent = %s;
text.style.cssText = 'position:absolute;left:' + %s + 'px;top:' + %s + 'px;background:rgba(59,130,246,0.78);color:#fff;font:11px sans-serif;padding:1px 4px;white-space:nowrap;';
box.appendChild(dot); box.appendChild(crossH); box.appendChild(crossV); box.appendChild(text);
document.documentElement.appendChild(box);
return 'ok';
})()`,
			pyFloatJS(x), pyFloatJS(y),
			pyFloatJS(-r), pyFloatJS(-r), pyFloatJS(2*r), pyFloatJS(2*r),
			pyFloatJS(-r-4), pyFloatJS(-1), pyFloatJS(2*r+8),
			pyFloatJS(-1), pyFloatJS(-r-4), pyFloatJS(2*r+8),
			jsString(displayLabel), pyFloatJS(12), pyFloatJS(-r-18))
	}

	return fmt.Sprintf(`(function(){
var old = document.getElementById('__hive_shot_hl');
if (old) old.remove();
var box = document.createElement('div');
box.id = '__hive_shot_hl';
box.style.cssText = 'position:fixed;left:' + %s + 'px;top:' + %s + 'px;width:' + %s + 'px;height:' + %s + 'px;z-index:2147483647;pointer-events:none;box-sizing:border-box;background:rgba(59,130,246,0.27);border:2px solid rgba(59,130,246,0.86);';
var text = document.createElement('div');
text.textContent = %s;
text.style.cssText = 'position:absolute;left:0;top:' + %s + 'px;background:rgba(59,130,246,0.78);color:#fff;font:11px sans-serif;padding:1px 4px;white-space:nowrap;';
box.appendChild(text);
document.documentElement.appendChild(box);
return 'ok';
})()`,
		pyFloatJS(x), pyFloatJS(y), pyFloatJS(w), pyFloatJS(hh),
		jsString(displayLabel), pyFloatJS(-18))
}

// pyFloatJS renders a number into a JS expression body.
func pyFloatJS(v float64) string {
	return fmt.Sprintf("%g", v)
}

const removeHighlightOverlayJS = `(function(){
var el = document.getElementById('__hive_shot_hl');
if (el) el.remove();
return 'ok';
})()`
