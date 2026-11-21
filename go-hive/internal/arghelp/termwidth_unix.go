//go:build !windows

package arghelp

import (
	"os"
	"syscall"
	"unsafe"
)

// terminalColumns asks the kernel for the stdout window size, falling back to
// stderr and stdin; 0 means "not a tty" (argparse's os.get_terminal_size
// raises there and shutil falls back to 80).
func terminalColumns() int {
	for _, file := range []*os.File{os.Stdout, os.Stderr, os.Stdin} {
		if file == nil {
			continue
		}
		width, _, err := windowSize(file.Fd())
		if err == nil && width > 0 {
			return width
		}
	}
	return 0
}

func windowSize(fd uintptr) (int, int, error) {
	var size struct {
		rows, cols, xpixel, ypixel uint16
	}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		fd,
		uintptr(syscall.TIOCGWINSZ),
		uintptr(unsafe.Pointer(&size)),
	)
	if errno != 0 {
		return 0, 0, errno
	}
	return int(size.cols), int(size.rows), nil
}
