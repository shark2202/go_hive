package charts

// The render page is a faithful port of renderer.py's _build_html shell plus
// the two in-page render scripts Playwright used to evaluate. Chrome loads the
// page in headless mode, the script renders the spec and records the outcome
// on the <html> element, and the DOM dump that travels back with the
// screenshot is how Go learns whether the render succeeded.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// buildPage assembles the HTML document for one render.
func buildPage(kind string, spec any, width, height int, theme string) (string, error) {
	if kind == "echarts" {
		if _, isString := spec.(string); isString {
			// chart_render coerces JSON-string specs before this point; a
			// string here would reach echarts.setOption and blow up the way
			// the 2026-05-01 user report described.
			return "", &RenderError{Message: "spec arrived as a string; it should have been parsed to a dict in chart_render"}
		}
		if _, err := json.Marshal(spec); err != nil {
			return "", &RenderError{Message: "spec is not JSON-serializable: " + err.Error()}
		}
	}
	if kind == "mermaid" {
		if _, isString := spec.(string); !isString {
			return "", &RenderError{Message: "mermaid spec must be a string"}
		}
	}

	background, foreground := pageColors(theme)
	var script string
	if kind == "echarts" {
		script = echartsPageScript(spec, theme)
	} else {
		script = mermaidPageScript(spec.(string), theme)
	}
	// The literal </script is the one sequence that must never appear inside
	// an inline <script> block; in JS source it is only meaningful inside
	// strings or regexes, where \/ is the same character.
	script = strings.ReplaceAll(script, "</script", `<\/script`)

	name := libraryFileName(kind)
	page := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8" />
<style>
  html, body {
    margin: 0; padding: 0;
    background: %s;
    color: %s;
    font-family: "Inter Tight", -apple-system, BlinkMacSystemFont,
                 "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  }
  #chart {
    width: %dpx;
    height: %dpx;
    background: %s;
  }
  /* Mermaid centres its SVG inside the container by default */
  .mermaid { display: flex; justify-content: center; align-items: center; }
</style>
</head>
<body>
<div id="chart"></div>
<script src="%s"></script>
<script>%s</script>
</body>
</html>
`, background, foreground, width, height, background, name, script)
	return page, nil
}

// jsonLiteral renders a Go value as a JS string literal that carries the
// original JSON text; the page parses it with JSON.parse so a spec round-trips
// exactly the way Playwright serialized it.
func jsonLiteral(raw string) string {
	encoded, err := json.Marshal(raw)
	if err != nil {
		panic("charts: string literal not encodable: " + err.Error())
	}
	return string(encoded)
}

// echartsPageScript ports renderer.py's echarts evaluate body: register the
// OpenHive theme, disable animation, pin title/legend/grid to disjoint
// regions, then record success (or the thrown message) on <html>.
func echartsPageScript(spec any, theme string) string {
	optionJSON, err := json.Marshal(spec)
	if err != nil {
		// buildPage already validated serializability.
		optionJSON = []byte("null")
	}
	return fmt.Sprintf(`
document.documentElement.setAttribute('data-chart-status', 'running');
window.__chartReady = false;
try {
  echarts.registerTheme('openhive', JSON.parse(%s));
  const el = document.getElementById('chart');
  const chart = echarts.init(el, 'openhive', {renderer: 'svg'});

  // Disable all animations for the SSR render; without this the screenshot
  // fires mid-animation and most data points are missing.
  //
  // Disjoint-region layout policy: title anchored to TOP, legend to BOTTOM
  // (unless orient:'vertical'), grid in the middle with containLabel.
  // User-supplied vertical positions are stripped so an agent spec like
  // legend.top:"8%%" cannot collide with the title. Horizontal anchoring
  // (left/right) is preserved.
  const option = JSON.parse(%s);
  const userTitle = option.title || {};
  const userLegend = option.legend;
  const userGrid = option.grid || {};
  const legendVertical = userLegend && userLegend.orient === 'vertical';
  const stripV = (o) => {
    const c = Object.assign({}, o);
    delete c.top; delete c.bottom; return c;
  };
  const sanitized = Object.assign({}, option, {
    animation: false,
    animationDuration: 0,
    animationDurationUpdate: 0,
    animationEasing: 'linear',
    animationEasingUpdate: 'linear',
    title: Object.assign({left: 'center'}, stripV(userTitle), {top: 16}),
    grid: Object.assign({left: 56, right: 56}, stripV(userGrid), {
      top: 64,
      bottom: userLegend && !legendVertical ? 96 : 40,
      containLabel: true,
    }),
  });
  if (userLegend) {
    const legendDefaults = {
      icon: 'roundRect', itemWidth: 12, itemHeight: 12, itemGap: 16,
    };
    sanitized.legend = legendVertical
      ? Object.assign(legendDefaults, userLegend)
      : Object.assign(legendDefaults, stripV(userLegend), {bottom: 16});
  }

  // chart.on('finished') fires once the layout pass has settled. With
  // animation off this is typically synchronous, but ECharts may still
  // deliver it on a later frame. A render error is final: renderer.py's
  // evaluate returns {ok:false} before any later 'finished', so this handler
  // must never overwrite the error status.
  function markReady() {
    window.__chartReady = true;
    if (document.documentElement.getAttribute('data-chart-status') !== 'error') {
      document.documentElement.setAttribute('data-chart-status', 'ok');
    }
  }
  chart.on('finished', markReady);
  chart.setOption(sanitized);
  requestAnimationFrame(() => {
    requestAnimationFrame(markReady);
  });
} catch (e) {
  window.__chartError = String(e);
  document.documentElement.setAttribute('data-chart-error', String(e));
  document.documentElement.setAttribute('data-chart-status', 'error');
}
`, jsonLiteral(ThemeJSON(theme)), jsonLiteral(string(optionJSON)))
}

// mermaidPageScript ports renderer.py's mermaid evaluate body. theme selects
// mermaid's built-in dark theme in dark mode, 'default' otherwise.
func mermaidPageScript(source, theme string) string {
	mermaidTheme := "default"
	if theme == "dark" {
		mermaidTheme = "dark"
	}
	return fmt.Sprintf(`
document.documentElement.setAttribute('data-chart-status', 'running');
window.__chartReady = false;
(async () => {
  try {
    if (typeof mermaid === 'undefined') {
      // renderer.py returns the literal 'mermaid not loaded' (not a thrown
      // Error), and that string is what the agent sees after the prefix.
      window.__chartError = 'mermaid not loaded';
      document.documentElement.setAttribute('data-chart-error', 'mermaid not loaded');
      document.documentElement.setAttribute('data-chart-status', 'error');
      return;
    }
    mermaid.initialize({
      startOnLoad: false,
      theme: %s,
      securityLevel: 'loose',
    });
    const out = await mermaid.render('mmd-' + Math.random().toString(36).slice(2), %s);
    document.getElementById('chart').innerHTML = out.svg;
    window.__chartReady = true;
    document.documentElement.setAttribute('data-chart-status', 'ok');
  } catch (e) {
    window.__chartError = String(e);
    document.documentElement.setAttribute('data-chart-error', String(e));
    document.documentElement.setAttribute('data-chart-status', 'error');
  }
})();
`, jsonLiteral(mermaidTheme), jsonLiteral(source))
}
