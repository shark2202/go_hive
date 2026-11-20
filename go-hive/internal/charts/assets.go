package charts

// The chart libraries are copied verbatim from the upstream Hive tree
// (tools/src/chart_tools/static/) and embedded so a render never depends on
// the network. See assets/README.md for versions, licenses and checksums.

import _ "embed"

//go:embed assets/echarts.min.js
var echartsLibrary []byte

//go:embed assets/mermaid.min.js
var mermaidLibrary []byte

// libraryFileName returns the on-disk name the renderer writes next to the
// temporary page and the embedded bytes for the requested kind.
func libraryFileName(kind string) string {
	if kind == "mermaid" {
		return "mermaid.min.js"
	}
	return "echarts.min.js"
}

func libraryBytes(kind string) []byte {
	if kind == "mermaid" {
		return mermaidLibrary
	}
	return echartsLibrary
}
