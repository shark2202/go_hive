# Bundled chart render libraries

These two files are byte-identical copies of the static assets shipped by the
upstream Hive tree at `tools/src/chart_tools/static/`. They are embedded into
the Go binary (`internal/charts/assets.go`, `//go:embed`) and written next to
the temporary page the headless Chrome renderer loads, so chart renders never
touch the network (the Python renderer falls back to a CDN only when its own
static copy is missing).

| File | Version (self-reported) | License | Upstream | SHA-256 |
| --- | --- | --- | --- | --- |
| `echarts.min.js` | Apache ECharts 5.6.0 | Apache-2.0 | https://github.com/apache/echarts | `e84270bd0cd5bdf60fefc26d00c2a391cb2e81f4d26a7a9ee16185a54773a3cf` |
| `mermaid.min.js` | Mermaid 11.4.0 | MIT | https://github.com/mermaid-js/mermaid | `1bca2e3c09f0e33ccc6e701e9c79d5cfa87d5e929b35d3a4aea4e1ec069dc87b` |

Notes:

- `echarts.min.js` keeps its upstream Apache-2.0 header, including the embedded
  Microsoft TypeScript runtime notice. Apache-2.0 requires retaining the
  LICENSE and NOTICE files of the distribution; see `THIRD_PARTY_NOTICES.md`
  at the module root.
- `mermaid.min.js` is a minified esbuild bundle; it carries its bundled
  dependencies' MIT headers. Mermaid itself is MIT.
- Both files are minified upstream artifacts. Do not reformat or edit them:
  the renderer's visual parity with the Python (`Playwright`) tool is only
  meaningful while the library bytes match upstream.
- Version note: `tools/src/chart_tools/renderer.py` names the CDN fallbacks as
  echarts 5.5.1 / mermaid 11.4.0, but the bundled `echarts.min.js` reports
  5.6.0. The bundle is the source of truth for what gets rendered.
