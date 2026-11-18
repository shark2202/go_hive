// Package uiembed exposes the dashboard build that ships inside a binary
// compiled with -tags embed_ui.
//
// Builds without the tag (and builds whose staged tree is missing or has no
// index.html) report no assets, so the server keeps its on-disk discovery.
package uiembed

import "io/fs"

// Assets returns the embedded dashboard filesystem and true only when this
// binary was compiled with -tags embed_ui and the staged dist tree contains
// an index.html. Otherwise it reports (nil, false).
func Assets() (fs.FS, bool) {
	return assets()
}
