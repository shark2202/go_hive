package charts

// Minimal SVG document builder. The rasterizer turns this into the PNG the
// tool returns; the same markup is also what the SVG fallback writes to disk.
// Everything is drawn in logical chart coordinates while the root element
// carries the device-pixel size, so a 96 dpi export is 1:1 and a 300 dpi
// export scales cleanly without re-laying-out text.

import (
	"fmt"
	"strings"
)

type svgDoc struct {
	b        strings.Builder
	logicalW float64
	logicalH float64
}

func newSVGDoc(pixelW, pixelH int, logicalW, logicalH float64, background string) *svgDoc {
	doc := &svgDoc{logicalW: logicalW, logicalH: logicalH}
	doc.b.WriteString(fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %s %s">`,
		pixelW, pixelH, svgNum(logicalW), svgNum(logicalH),
	))
	if background != "" {
		doc.b.WriteString(fmt.Sprintf(`<rect x="0" y="0" width="%s" height="%s" fill="%s"/>`,
			svgNum(logicalW), svgNum(logicalH), background))
	}
	return doc
}

func (d *svgDoc) String() string {
	return d.b.String() + `</svg>`
}

func (d *svgDoc) rect(x, y, w, h float64, fill string, rx float64) {
	if w < 0 {
		w = 0
	}
	if h < 0 {
		h = 0
	}
	if rx > 0 {
		d.b.WriteString(fmt.Sprintf(`<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s"/>`,
			svgNum(x), svgNum(y), svgNum(w), svgNum(h), svgNum(rx), fill))
		return
	}
	d.b.WriteString(fmt.Sprintf(`<rect x="%s" y="%s" width="%s" height="%s" fill="%s"/>`,
		svgNum(x), svgNum(y), svgNum(w), svgNum(h), fill))
}

func (d *svgDoc) strokeRect(x, y, w, h float64, stroke string, width, rx float64) {
	d.b.WriteString(fmt.Sprintf(
		`<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="none" stroke="%s" stroke-width="%s"/>`,
		svgNum(x), svgNum(y), svgNum(w), svgNum(h), svgNum(rx), stroke, svgNum(width)))
}

func (d *svgDoc) line(x1, y1, x2, y2 float64, stroke string, width float64, dash string) {
	extra := ""
	if dash != "" {
		extra = fmt.Sprintf(` stroke-dasharray="%s"`, dash)
	}
	d.b.WriteString(fmt.Sprintf(`<line x1="%s" y1="%s" x2="%s" y2="%s" stroke="%s" stroke-width="%s"%s/>`,
		svgNum(x1), svgNum(y1), svgNum(x2), svgNum(y2), stroke, svgNum(width), extra))
}

func (d *svgDoc) circle(cx, cy, r float64, fill, stroke string, strokeWidth float64) {
	if stroke == "" {
		d.b.WriteString(fmt.Sprintf(`<circle cx="%s" cy="%s" r="%s" fill="%s"/>`,
			svgNum(cx), svgNum(cy), svgNum(r), fill))
		return
	}
	d.b.WriteString(fmt.Sprintf(`<circle cx="%s" cy="%s" r="%s" fill="%s" stroke="%s" stroke-width="%s"/>`,
		svgNum(cx), svgNum(cy), svgNum(r), fill, stroke, svgNum(strokeWidth)))
}

func (d *svgDoc) polyline(points [][2]float64, stroke string, width float64) {
	if len(points) < 2 {
		return
	}
	var b strings.Builder
	for i, point := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(svgNum(point[0]) + "," + svgNum(point[1]))
	}
	d.b.WriteString(fmt.Sprintf(`<polyline points="%s" fill="none" stroke="%s" stroke-width="%s" stroke-linejoin="round" stroke-linecap="round"/>`,
		b.String(), stroke, svgNum(width)))
}

func (d *svgDoc) polygon(points [][2]float64, fill, stroke string, strokeWidth float64) {
	if len(points) < 3 {
		return
	}
	var b strings.Builder
	for i, point := range points {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(svgNum(point[0]) + "," + svgNum(point[1]))
	}
	d.b.WriteString(fmt.Sprintf(`<polygon points="%s" fill="%s" stroke="%s" stroke-width="%s" stroke-linejoin="round"/>`,
		b.String(), fill, stroke, svgNum(strokeWidth)))
}

func (d *svgDoc) path(data, fill, stroke string, strokeWidth float64, opacity float64) {
	opacityAttr := ""
	if opacity > 0 && opacity < 1 {
		opacityAttr = fmt.Sprintf(` fill-opacity="%s"`, svgNum(opacity))
	}
	d.b.WriteString(fmt.Sprintf(`<path d="%s" fill="%s" stroke="%s" stroke-width="%s"%s/>`,
		data, fill, stroke, svgNum(strokeWidth), opacityAttr))
}

func (d *svgDoc) text(x, y float64, content, anchor, color string, size float64, weight int) {
	d.textOpacity(x, y, content, anchor, color, size, weight, 1)
}

func (d *svgDoc) textOpacity(x, y float64, content, anchor, color string, size float64, weight int, opacity float64) {
	opacityAttr := ""
	if opacity < 1 {
		opacityAttr = fmt.Sprintf(` opacity="%s"`, svgNum(opacity))
	}
	d.b.WriteString(fmt.Sprintf(
		`<text x="%s" y="%s" text-anchor="%s" font-family="%s" font-size="%s" font-weight="%d" fill="%s"%s>%s</text>`,
		svgNum(x), svgNum(y), anchor, svgFontFamily, svgNum(size), weight, color, opacityAttr, svgEscape(content)))
}

const svgFontFamily = `Inter Tight, -apple-system, BlinkMacSystemFont, Segoe UI, Roboto, Helvetica Neue, Arial, sans-serif`

// svgEscape escapes text for XML content and attribute values.
func svgEscape(value string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return replacer.Replace(value)
}

// svgNum renders a float compactly: 12 instead of 12.000000.
func svgNum(value float64) string {
	if value == float64(int64(value)) {
		return fmt.Sprintf("%d", int64(value))
	}
	return strconvFormat(value)
}

func strconvFormat(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", value), "0"), ".")
}

// estimateTextWidth approximates rendered text width for layout decisions.
func estimateTextWidth(text string, size float64) float64 {
	runes := []rune(text)
	width := 0.0
	for _, r := range runes {
		switch {
		case r < 0x2E80:
			width += size * 0.56
		default:
			// CJK glyphs are square.
			width += size
		}
	}
	return width
}

func truncateLabel(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}
