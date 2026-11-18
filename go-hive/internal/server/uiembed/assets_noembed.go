//go:build !embed_ui

package uiembed

import "io/fs"

func assets() (fs.FS, bool) { return nil, false }
