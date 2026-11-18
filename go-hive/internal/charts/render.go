package charts

// buildSVG dispatches a spec to the right renderer.
func buildSVG(kind string, spec any, width, height, pixelW, pixelH int, themeName string) (string, error) {
	theme := chartThemeFor(themeName)
	switch kind {
	case "echarts":
		return renderEChartsSVG(spec, width, height, pixelW, pixelH, theme)
	case "mermaid":
		return renderMermaidSVG(spec, width, height, pixelW, pixelH, theme)
	}
	return "", &RenderError{Message: "kind must be 'echarts' or 'mermaid', got " + quote(kind)}
}
