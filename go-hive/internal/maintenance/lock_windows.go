//go:build windows

package maintenance

import "os"

func lockFile(f *os.File) func() {
	return func() {}
}
