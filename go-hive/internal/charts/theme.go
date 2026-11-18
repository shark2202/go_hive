package charts

// OpenHive chart theme, ported from tools/src/chart_tools/theme.py. The
// palette and spacing defaults are what make agent specs look branded instead
// of hello-world; an explicit field in the spec still wins.

import (
	"os"
	"strings"
)

type chartTheme struct {
	Name       string
	Background string
	Foreground string
	Muted      string
	GridLine   string
	AxisLine   string
	Palette    []string
}

var chartThemeLight = chartTheme{
	Name:       "light",
	Background: "#ffffff",
	Foreground: "#1a1a1a",
	Muted:      "#6b6b6b",
	GridLine:   "#ebe9e2",
	AxisLine:   "#d0cfca",
	Palette: []string{
		"#db6f02", // honey orange (primary)
		"#456a8d", // slate blue
		"#3d7a4a", // sage green
		"#a8453d", // terracotta brick
		"#c48820", // warm bronze
		"#5d5b88", // indigo
		"#7d6b51", // olive
		"#8e4200", // rust
	},
}

var chartThemeDark = chartTheme{
	Name:       "dark",
	Background: "#0e0e0d",
	Foreground: "#e8e6e0",
	Muted:      "#8a8a8a",
	GridLine:   "#2a2724",
	AxisLine:   "#3a3733",
	Palette: []string{
		"#ffb825", // bright honey
		"#7ba2c4", // cool slate
		"#7bb285", // bright sage
		"#d97470", // warm coral
		"#e0a83a", // bright bronze
		"#9892c4", // bright indigo
		"#b8a685", // warm taupe
		"#d97e3a", // bright rust
	},
}

// ResolveThemeName mirrors _system_theme: the desktop sets HIVE_DESKTOP_THEME
// from nativeTheme.shouldUseDarkColors; anything but "dark" means light.
func ResolveThemeName() string {
	if strings.ToLower(strings.TrimSpace(os.Getenv("HIVE_DESKTOP_THEME"))) == "dark" {
		return "dark"
	}
	return "light"
}

func chartThemeFor(name string) chartTheme {
	if name == "dark" {
		return chartThemeDark
	}
	return chartThemeLight
}

func (t chartTheme) color(index int) string {
	if len(t.Palette) == 0 {
		return t.Foreground
	}
	return t.Palette[index%len(t.Palette)]
}
