package charts

// OpenHive chart theme, ported from tools/src/chart_tools/theme.py. The
// palette and spacing defaults are what make agent specs look branded instead
// of hello-world; an explicit field in the spec still wins.

import (
	"encoding/json"
	"os"
	"strings"
)

// Brand palette — categorical, alternating warm/cool for adjacent-series
// distinguishability (upstream _BRAND_PALETTE_LIGHT / _BRAND_PALETTE_DARK).
var (
	brandPaletteLight = []string{
		"#db6f02", // honey orange (primary)
		"#456a8d", // slate blue
		"#3d7a4a", // sage green
		"#a8453d", // terracotta brick
		"#c48820", // warm bronze
		"#5d5b88", // indigo
		"#7d6b51", // olive
		"#8e4200", // rust
	}
	brandPaletteDark = []string{
		"#ffb825", // bright honey
		"#7ba2c4", // cool slate
		"#7bb285", // bright sage
		"#d97470", // warm coral
		"#e0a83a", // bright bronze
		"#9892c4", // bright indigo
		"#b8a685", // warm taupe
		"#d97e3a", // bright rust
	}
)

const chartFontFamily = `"Inter Tight", -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif`

// ResolveThemeName mirrors _system_theme: the desktop sets HIVE_DESKTOP_THEME
// from nativeTheme.shouldUseDarkColors; anything but "dark" means light.
//
// Parity: tools/src/chart_tools/tools.py::_system_theme
func ResolveThemeName() string {
	if strings.ToLower(strings.TrimSpace(os.Getenv("HIVE_DESKTOP_THEME"))) == "dark" {
		return "dark"
	}
	return "light"
}

// pageColors are the solid page/background colors from renderer.py's
// _build_html: a saved chart must work on light and dark embed surfaces.
//
// Parity: tools/src/chart_tools/renderer.py::_build_html
func pageColors(theme string) (background, foreground string) {
	if theme == "dark" {
		return "#0e0e0d", "#e8e6e0"
	}
	return "#ffffff", "#1a1a1a"
}

// Theme builds the OpenHive ECharts theme dict for the given mode, matching
// theme.py::build_theme field for field.
//
// Parity: tools/src/chart_tools/theme.py::build_theme
func Theme(name string) map[string]any {
	isDark := name == "dark"
	fg := "#1a1a1a"
	fgMuted := "#6b6b6b"
	gridLine := "#ebe9e2"
	axisLine := "#d0cfca"
	tooltipBackground := "#ffffff"
	tooltipBorder := "#d0cfca"
	palette := brandPaletteLight
	if isDark {
		fg, fgMuted = "#e8e6e0", "#8a8a8a"
		gridLine, axisLine = "#2a2724", "#3a3733"
		tooltipBackground, tooltipBorder = "#181715", "#2a2724"
		palette = brandPaletteDark
	}

	return map[string]any{
		"color":           append([]string(nil), palette...),
		"backgroundColor": "transparent",
		"textStyle": map[string]any{
			"fontFamily": chartFontFamily,
			"color":      fg,
			"fontSize":   12,
		},
		// Title sits cozy at the top with breathing room from legend and axes.
		"title": map[string]any{
			"left":         "center",
			"top":          32,
			"textStyle":    map[string]any{"color": fg, "fontSize": 18, "fontWeight": 600},
			"subtextStyle": map[string]any{"color": fgMuted, "fontSize": 13},
		},
		"legend": map[string]any{
			"top":        72,
			"icon":       "roundRect",
			"itemWidth":  12,
			"itemHeight": 12,
			"itemGap":    20,
			"textStyle":  map[string]any{"color": fgMuted, "fontSize": 12},
		},
		// The plot area keeps real margins: top 130 leaves room for title +
		// legend, bottom 80 fits two-line x-axis labels plus an axis name.
		"grid": map[string]any{
			"top":          130,
			"left":         56,
			"right":        56,
			"bottom":       80,
			"containLabel": true,
		},
		"categoryAxis": categoryAxisTheme(fgMuted, axisLine),
		"valueAxis":    valueAxisTheme(fgMuted, gridLine),
		"logAxis":      logAxisTheme(fgMuted, gridLine),
		"timeAxis":     timeAxisTheme(fgMuted, axisLine),
		"tooltip": map[string]any{
			"backgroundColor": tooltipBackground,
			"borderColor":     tooltipBorder,
			"borderWidth":     1,
			"padding":         []any{8, 12},
			"textStyle":       map[string]any{"color": fg, "fontSize": 12},
			"axisPointer": map[string]any{
				"lineStyle":  map[string]any{"color": axisLine, "type": "dashed"},
				"crossStyle": map[string]any{"color": axisLine},
			},
		},
		"bar": map[string]any{
			"itemStyle": map[string]any{"borderRadius": []any{3, 3, 0, 0}},
		},
		"line": map[string]any{
			"lineStyle":  map[string]any{"width": 2.5},
			"symbol":     "circle",
			"symbolSize": 6,
		},
		"candlestick": map[string]any{
			"itemStyle": map[string]any{
				"color":        "#3d7a4a",
				"color0":       "#a8453d",
				"borderColor":  "#3d7a4a",
				"borderColor0": "#a8453d",
			},
		},
	}
}

// The four axis blocks differ in which keys they carry upstream, so each is
// built explicitly instead of through one shared skeleton.

func categoryAxisTheme(fgMuted, axisLine string) map[string]any {
	return map[string]any{
		"axisLine":      map[string]any{"show": true, "lineStyle": map[string]any{"color": axisLine}},
		"axisTick":      map[string]any{"show": false},
		"axisLabel":     map[string]any{"color": fgMuted, "fontSize": 11, "margin": 14},
		"splitLine":     map[string]any{"show": false},
		"nameLocation":  "middle",
		"nameGap":       38,
		"nameTextStyle": map[string]any{"color": fgMuted, "fontSize": 12},
	}
}

func valueAxisTheme(fgMuted, gridLine string) map[string]any {
	return map[string]any{
		"axisLine":      map[string]any{"show": false},
		"axisTick":      map[string]any{"show": false},
		"axisLabel":     map[string]any{"color": fgMuted, "fontSize": 11, "margin": 14},
		"splitLine":     map[string]any{"lineStyle": map[string]any{"color": gridLine, "type": "dashed"}},
		"nameLocation":  "middle",
		"nameGap":       44,
		"nameTextStyle": map[string]any{"color": fgMuted, "fontSize": 12, "fontWeight": 500},
	}
}

func logAxisTheme(fgMuted, gridLine string) map[string]any {
	return map[string]any{
		"axisLine":      map[string]any{"show": false},
		"axisLabel":     map[string]any{"color": fgMuted, "fontSize": 11},
		"splitLine":     map[string]any{"lineStyle": map[string]any{"color": gridLine, "type": "dashed"}},
		"nameLocation":  "middle",
		"nameGap":       44,
		"nameTextStyle": map[string]any{"color": fgMuted, "fontSize": 12, "fontWeight": 500},
	}
}

func timeAxisTheme(fgMuted, axisLine string) map[string]any {
	return map[string]any{
		"axisLine":      map[string]any{"show": true, "lineStyle": map[string]any{"color": axisLine}},
		"axisLabel":     map[string]any{"color": fgMuted, "fontSize": 11, "margin": 14},
		"splitLine":     map[string]any{"show": false},
		"nameLocation":  "middle",
		"nameGap":       38,
		"nameTextStyle": map[string]any{"color": fgMuted, "fontSize": 12},
	}
}

// ThemeJSON renders the theme as the JSON string the page hands to
// echarts.registerTheme('openhive', JSON.parse(themeJson)).
//
// Parity: tools/src/chart_tools/theme.py::theme_json
func ThemeJSON(name string) string {
	encoded, err := json.Marshal(Theme(name))
	if err != nil {
		// All values are JSON-safe literals; an error here would mean the
		// theme table itself was built wrong, so fail loud rather than render
		// an unthemed chart.
		panic("charts: theme is not JSON-serializable: " + err.Error())
	}
	return string(encoded)
}
