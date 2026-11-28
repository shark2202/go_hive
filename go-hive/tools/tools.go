//go:build tools

// Package tools pins the developer tools this module builds from source.
//
// It is a separate Go module so the main module's dependency graph stays
// unchanged: nothing here is linked into a released hive binary. Build the
// pinned toolchain with `make tools` from the module root.
//
// The blank imports keep the tool packages in this module's go.mod/go.sum;
// without them `go mod tidy` would drop the requirement.
package tools

import (
	_ "golang.org/x/tools/cmd/deadcode"
)
