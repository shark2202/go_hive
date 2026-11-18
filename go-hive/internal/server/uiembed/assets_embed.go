//go:build embed_ui

package uiembed

import (
	"embed"
	"io/fs"
	"sync"
)

// dist is the frontend build staged by go_hive/scripts/embed-ui.sh.
//
// The staging copy exists because go:embed cannot reference files outside the
// package directory (../frontend/dist is rejected by the compiler).
//
//go:embed all:dist
var dist embed.FS

var (
	assetsOnce sync.Once
	assetsFS   fs.FS
	assetsOK   bool
)

func assets() (fs.FS, bool) {
	assetsOnce.Do(func() {
		sub, err := fs.Sub(dist, "dist")
		if err != nil {
			return
		}
		// An empty or partial stage must not claim the dashboard is available:
		// serving it would register the catch-all and then 404 every page.
		if _, err := fs.Stat(sub, "index.html"); err != nil {
			return
		}
		assetsFS, assetsOK = sub, true
	})
	return assetsFS, assetsOK
}
