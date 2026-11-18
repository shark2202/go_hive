package charts

// ECharts subset renderer: bar, line (with optional area) and pie. The layout
// follows the engine's sanitized option shape — title pinned to the top,
// legend to the bottom, a single plot grid in between — so charts read the
// same as the live in-chat mount even though the drawing code is local.

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type echartsSeries struct {
	Kind  string
	Name  string
	Data  []any
	Area  bool
	Donut bool
}

type echartsOption struct {
	Title      string
	Subtitle   string
	Categories []string
	XName      string
	YName      string
	Series     []echartsSeries
	ShowLegend bool
	YMin       *float64
	YMax       *float64
}

func parseEChartsOption(spec any) (*echartsOption, error) {
	root := asMap(spec)
	if root == nil {
		return nil, &RenderError{Message: "echarts spec must be a JSON object"}
	}
	opt := &echartsOption{ShowLegend: root["legend"] != nil}

	if title := asMap(root["title"]); title != nil {
		opt.Title = scalarString(title["text"])
		opt.Subtitle = scalarString(title["subtext"])
	}

	if xAxis := firstAxis(root["xAxis"]); xAxis != nil {
		opt.XName = scalarString(xAxis["name"])
		for _, raw := range asSlice(xAxis["data"]) {
			opt.Categories = append(opt.Categories, scalarString(raw))
		}
	}
	if yAxis := firstAxis(root["yAxis"]); yAxis != nil {
		opt.YName = scalarString(yAxis["name"])
		if value, ok := scalarFloat(yAxis["min"]); ok {
			opt.YMin = &value
		}
		if value, ok := scalarFloat(yAxis["max"]); ok {
			opt.YMax = &value
		}
	}

	for _, raw := range asSeriesList(root["series"]) {
		item := asMap(raw)
		if item == nil {
			continue
		}
		series := echartsSeries{
			Kind:  strings.ToLower(scalarStringDefault(item["type"], "line")),
			Name:  scalarString(item["name"]),
			Area:  item["areaStyle"] != nil,
			Donut: isDonutRadius(item["radius"]),
		}
		switch data := item["data"].(type) {
		case []any:
			series.Data = data
		default:
			series.Data = nil
		}
		opt.Series = append(opt.Series, series)
	}
	if len(opt.Series) == 0 {
		return nil, &RenderError{Message: "echarts spec has no series"}
	}
	for _, series := range opt.Series {
		switch series.Kind {
		case "bar", "line", "pie":
		default:
			return nil, &RenderError{Message: "unsupported series type " + quote(series.Kind) +
				"; this renderer supports bar, line and pie"}
		}
	}
	return opt, nil
}

func firstAxis(value any) map[string]any {
	if list := asSlice(value); list != nil {
		if len(list) == 0 {
			return nil
		}
		return asMap(list[0])
	}
	return asMap(value)
}

func asSeriesList(value any) []any {
	if list := asSlice(value); list != nil {
		return list
	}
	if single := asMap(value); single != nil {
		return []any{single}
	}
	return nil
}

func renderEChartsSVG(spec any, width, height, pixelW, pixelH int, theme chartTheme) (string, error) {
	opt, err := parseEChartsOption(spec)
	if err != nil {
		return "", err
	}
	allPie := true
	for _, series := range opt.Series {
		if series.Kind != "pie" {
			allPie = false
		}
	}
	if allPie {
		return renderPieSVG(opt, width, height, pixelW, pixelH, theme)
	}
	for _, series := range opt.Series {
		if series.Kind == "pie" {
			return "", &RenderError{Message: "pie series cannot be mixed with bar/line series in one chart"}
		}
	}
	return renderCartesianSVG(opt, width, height, pixelW, pixelH, theme)
}

// ── Cartesian (bar / line) ───────────────────────────────────────────

func renderCartesianSVG(opt *echartsOption, width, height, pixelW, pixelH int, theme chartTheme) (string, error) {
	if len(opt.Categories) == 0 {
		return "", &RenderError{Message: "xAxis.data is required for bar/line series"}
	}
	widthF, heightF := float64(width), float64(height)
	doc := newSVGDoc(pixelW, pixelH, widthF, heightF, theme.Background)

	drawTitle(doc, opt, widthF, theme)
	legendItems := []string{}
	for _, series := range opt.Series {
		if series.Name != "" {
			legendItems = append(legendItems, series.Name)
		}
	}
	showLegend := opt.ShowLegend && len(legendItems) > 0

	left := 56.0
	right := widthF - 56
	top := 64.0
	bottom := heightF - 40
	if showLegend {
		bottom = heightF - 96
	}
	plotW := right - left
	plotH := bottom - top
	if plotW <= 20 || plotH <= 20 {
		return "", &RenderError{Message: "canvas is too small for a bar/line chart"}
	}

	values := []float64{}
	for _, series := range opt.Series {
		for _, raw := range series.Data {
			if value, ok := seriesValue(raw); ok {
				values = append(values, value)
			}
		}
	}
	if len(values) == 0 {
		return "", &RenderError{Message: "series data has no numeric values"}
	}
	lo, hi, ticks := niceScale(values, opt.YMin, opt.YMax)
	scaleY := func(value float64) float64 {
		return bottom - (value-lo)/(hi-lo)*plotH
	}

	// Grid lines + value labels.
	for _, tick := range ticks {
		y := scaleY(tick)
		doc.line(left, y, right, y, theme.GridLine, 1, "4 4")
		doc.text(left-10, y+4, formatTick(tick), "end", theme.Muted, 11, 400)
	}
	doc.line(left, top, left, bottom, theme.AxisLine, 1, "")
	doc.line(left, bottom, right, bottom, theme.AxisLine, 1, "")

	// Category labels.
	band := plotW / float64(len(opt.Categories))
	labelEvery := 1
	if band < 34 {
		labelEvery = int(math.Ceil(34 / band))
	}
	for index, category := range opt.Categories {
		if index%labelEvery != 0 {
			continue
		}
		doc.text(left+band*(float64(index)+0.5), bottom+18, truncateLabel(category, 22), "middle", theme.Muted, 11, 400)
	}
	if opt.XName != "" && !showLegend {
		doc.text(left+plotW/2, bottom+36, opt.XName, "middle", theme.Muted, 12, 400)
	}
	if opt.YName != "" {
		doc.text(left, top-14, opt.YName, "start", theme.Muted, 12, 400)
	}

	seriesCount := 0
	for _, series := range opt.Series {
		if series.Kind != "pie" {
			seriesCount++
		}
	}
	groupWidth := band * 0.62
	barWidth := groupWidth / float64(max(seriesCount, 1))

	seriesIndex := 0
	for _, series := range opt.Series {
		color := theme.color(seriesIndex)
		seriesIndex++
		switch series.Kind {
		case "bar":
			for index, raw := range series.Data {
				value, ok := seriesValue(raw)
				if !ok {
					continue
				}
				y := scaleY(value)
				x := left + band*float64(index) + (band-groupWidth)/2 + barWidth*float64(seriesIndex-1)
				doc.rect(x, y, barWidth*0.9, bottom-y, color, 3)
			}
		case "line":
			points := make([][2]float64, 0, len(series.Data))
			for index, raw := range series.Data {
				value, ok := seriesValue(raw)
				if !ok {
					continue
				}
				points = append(points, [2]float64{left + band*(float64(index)+0.5), scaleY(value)})
			}
			if series.Area && len(points) > 1 {
				polygon := make([][2]float64, 0, len(points)+2)
				polygon = append(polygon, points...)
				polygon = append(polygon, [2]float64{points[len(points)-1][0], bottom})
				polygon = append(polygon, [2]float64{points[0][0], bottom})
				doc.path(polygonPath(polygon), color, "none", 0, 0.15)
			}
			doc.polyline(points, color, 2.5)
			for _, point := range points {
				doc.circle(point[0], point[1], 3.5, theme.Background, color, 2)
			}
		}
	}

	if showLegend {
		drawLegend(doc, legendItems, widthF, heightF, theme)
	}
	return doc.String(), nil
}

// polygonPath is only used to paint a translucent area under a line.
func polygonPath(points [][2]float64) string {
	if len(points) < 3 {
		return ""
	}
	var b strings.Builder
	b.WriteString("M " + svgNum(points[0][0]) + " " + svgNum(points[0][1]))
	for _, point := range points[1:] {
		b.WriteString(" L " + svgNum(point[0]) + " " + svgNum(point[1]))
	}
	b.WriteString(" Z")
	return b.String()
}

// ── Pie ──────────────────────────────────────────────────────────────

type pieSlice struct {
	Name  string
	Value float64
}

func renderPieSVG(opt *echartsOption, width, height, pixelW, pixelH int, theme chartTheme) (string, error) {
	series := opt.Series[0]
	slices := []pieSlice{}
	total := 0.0
	for _, raw := range series.Data {
		item := asMap(raw)
		if item == nil {
			if value, ok := seriesValue(raw); ok {
				slices = append(slices, pieSlice{Name: formatTick(value), Value: value})
				total += value
			}
			continue
		}
		value, ok := seriesValue(item["value"])
		if !ok {
			continue
		}
		slices = append(slices, pieSlice{Name: scalarString(item["name"]), Value: value})
		total += value
	}
	if len(slices) == 0 || total <= 0 {
		return "", &RenderError{Message: "pie series has no positive numeric data"}
	}

	widthF, heightF := float64(width), float64(height)
	doc := newSVGDoc(pixelW, pixelH, widthF, heightF, theme.Background)
	drawTitle(doc, opt, widthF, theme)

	top := 64.0
	bottom := heightF - 24
	if opt.ShowLegend {
		bottom = heightF - 64
	}
	cx := widthF / 2
	cy := (top + bottom) / 2
	radius := math.Min(widthF, bottom-top) * 0.36
	if radius < 40 {
		return "", &RenderError{Message: "canvas is too small for a pie chart"}
	}
	inner := 0.0
	if series.Donut {
		inner = radius * 0.55
	}

	angle := -math.Pi / 2
	for index, slice := range slices {
		sweep := slice.Value / total * 2 * math.Pi
		end := angle + sweep
		color := theme.color(index)
		if inner > 0 {
			doc.path(pieDonutPath(cx, cy, radius, inner, angle, end, sweep > math.Pi), color, theme.Background, 1, 1)
		} else {
			doc.path(pieSlicePath(cx, cy, radius, angle, end, sweep > math.Pi), color, theme.Background, 1, 1)
		}
		if sweep > 0.18 {
			mid := angle + sweep/2
			lx := cx + (radius+14)*math.Cos(mid)
			ly := cy + (radius+14)*math.Sin(mid)
			anchor := "start"
			if math.Cos(mid) < 0 {
				anchor = "end"
			}
			label := slice.Name
			if label == "" {
				label = formatTick(slice.Value)
			}
			doc.text(lx, ly+4, truncateLabel(label, 18), anchor, theme.Foreground, 11, 400)
		}
		angle = end
	}

	if opt.ShowLegend {
		names := make([]string, 0, len(slices))
		for _, slice := range slices {
			if slice.Name != "" {
				names = append(names, slice.Name)
			}
		}
		drawLegend(doc, names, widthF, heightF, theme)
	}
	return doc.String(), nil
}

func pieSlicePath(cx, cy, radius, start, end float64, large bool) string {
	largeFlag := 0
	if large {
		largeFlag = 1
	}
	x1, y1 := cx+radius*math.Cos(start), cy+radius*math.Sin(start)
	x2, y2 := cx+radius*math.Cos(end), cy+radius*math.Sin(end)
	return fmt.Sprintf("M %s %s L %s %s A %s %s 0 %d 1 %s %s Z",
		svgNum(cx), svgNum(cy), svgNum(x1), svgNum(y1), svgNum(radius), svgNum(radius), largeFlag, svgNum(x2), svgNum(y2))
}

func pieDonutPath(cx, cy, outer, inner, start, end float64, large bool) string {
	largeFlag := 0
	if large {
		largeFlag = 1
	}
	ox1, oy1 := cx+outer*math.Cos(start), cy+outer*math.Sin(start)
	ox2, oy2 := cx+outer*math.Cos(end), cy+outer*math.Sin(end)
	ix1, iy1 := cx+inner*math.Cos(start), cy+inner*math.Sin(start)
	ix2, iy2 := cx+inner*math.Cos(end), cy+inner*math.Sin(end)
	return fmt.Sprintf("M %s %s A %s %s 0 %d 1 %s %s L %s %s A %s %s 0 %d 0 %s %s Z",
		svgNum(ox1), svgNum(oy1), svgNum(outer), svgNum(outer), largeFlag, svgNum(ox2), svgNum(oy2),
		svgNum(ix2), svgNum(iy2), svgNum(inner), svgNum(inner), largeFlag, svgNum(ix1), svgNum(iy1))
}

// ── Shared pieces ────────────────────────────────────────────────────

func drawTitle(doc *svgDoc, opt *echartsOption, width float64, theme chartTheme) {
	if opt.Title != "" {
		doc.text(width/2, 34, opt.Title, "middle", theme.Foreground, 18, 600)
	}
	if opt.Subtitle != "" {
		doc.text(width/2, 52, opt.Subtitle, "middle", theme.Muted, 13, 400)
	}
}

func drawLegend(doc *svgDoc, items []string, width, height float64, theme chartTheme) {
	if len(items) == 0 {
		return
	}
	const swatch = 12.0
	const gap = 20.0
	widths := make([]float64, len(items))
	total := 0.0
	for index, item := range items {
		widths[index] = swatch + 6 + estimateTextWidth(item, 12)
		total += widths[index]
		if index > 0 {
			total += gap
		}
	}
	x := (width - total) / 2
	y := height - 16 - swatch
	for index, item := range items {
		doc.rect(x, y, swatch, swatch, theme.color(index), 3)
		doc.text(x+swatch+6, y+swatch-2, item, "start", theme.Muted, 12, 400)
		x += widths[index] + gap
	}
}

// niceScale returns a zero-based (when positive) axis with 4–6 readable ticks.
func niceScale(values []float64, minOverride, maxOverride *float64) (float64, float64, []float64) {
	lo, hi := values[0], values[0]
	for _, value := range values {
		if value < lo {
			lo = value
		}
		if value > hi {
			hi = value
		}
	}
	if minOverride != nil {
		lo = *minOverride
	} else if lo > 0 {
		lo = 0
	}
	if maxOverride != nil {
		hi = *maxOverride
	} else if hi < 0 {
		hi = 0
	}
	if hi == lo {
		if hi == 0 {
			hi = 1
		} else if hi > 0 {
			hi = hi * 1.1
		} else {
			lo = lo * 1.1
		}
	}
	span := hi - lo
	stepGuess := span / 4
	magnitude := math.Pow(10, math.Floor(math.Log10(stepGuess)))
	residual := stepGuess / magnitude
	step := magnitude
	switch {
	case residual <= 1:
		step = magnitude
	case residual <= 2:
		step = 2 * magnitude
	case residual <= 5:
		step = 5 * magnitude
	default:
		step = 10 * magnitude
	}
	niceLo := math.Floor(lo/step) * step
	niceHi := math.Ceil(hi/step) * step
	ticks := []float64{}
	for value, guard := niceLo, 0; value <= niceHi+step/2 && guard < 12; value, guard = value+step, guard+1 {
		ticks = append(ticks, math.Round(value*1e9)/1e9)
	}
	return niceLo, niceHi, ticks
}

func formatTick(value float64) string {
	if math.Abs(value) >= 1000 && value == math.Trunc(value) {
		return strconv.FormatFloat(value, 'f', 0, 64)
	}
	return strconv.FormatFloat(math.Round(value*1e6)/1e6, 'f', -1, 64)
}

// ── Small value helpers ──────────────────────────────────────────────

func asMap(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func asSlice(value any) []any {
	if list, ok := value.([]any); ok {
		return list
	}
	return nil
}

func scalarString(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		return v
	case float64:
		return formatTick(v)
	case bool:
		if v {
			return "true"
		}
		return "false"
	}
	return fmt.Sprint(value)
}

func scalarStringDefault(value any, fallback string) string {
	if text := scalarString(value); text != "" {
		return text
	}
	return fallback
}

func scalarFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return parsed, err == nil
	}
	return 0, false
}

func seriesValue(raw any) (float64, bool) {
	if value, ok := scalarFloat(raw); ok {
		return value, true
	}
	if item := asMap(raw); item != nil {
		return scalarFloat(item["value"])
	}
	return 0, false
}

// isDonutRadius treats an ECharts two-element radius (or a plain "50%"
// inner radius string) as a donut.
func isDonutRadius(value any) bool {
	if list := asSlice(value); len(list) == 2 {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.Contains(text, "%") && strings.TrimSpace(text) != ""
	}
	return false
}
