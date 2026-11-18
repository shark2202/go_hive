//go:build darwin

package builtin

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Darwin tty ioctls. TIOCPTYNAME reports the slave name; TIOCPTYGNAME is the
// deprecated 128-byte spelling kept as a fallback for older kernels.
const (
	terminalDarwinTIOCPTYGRANT = 0x20007454
	terminalDarwinTIOCPTYUNLK  = 0x20007452
	terminalDarwinTIOCPTYNAME  = 0x44007462
	terminalDarwinTIOCPTYGNAME = 0x40807453
	terminalDarwinTIOCSWINSZ   = 0x80087467
)

func terminalDarwinIoctl(fd int, request, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

// terminalOpenPTY returns the master and slave ends of a fresh pseudo-terminal.
func terminalOpenPTY() (*os.File, *os.File, error) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY, 0o666)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	cleanup := func() {
		_ = syscall.Close(masterFD)
	}
	if err := terminalDarwinIoctl(masterFD, terminalDarwinTIOCPTYGRANT, 0); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("grantpt: %w", err)
	}
	if err := terminalDarwinIoctl(masterFD, terminalDarwinTIOCPTYUNLK, 0); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}

	nameBuf := make([]byte, 1024)
	if err := terminalDarwinIoctl(masterFD, terminalDarwinTIOCPTYNAME, uintptr(unsafe.Pointer(&nameBuf[0]))); err != nil {
		nameBuf = make([]byte, 128)
		if legacyErr := terminalDarwinIoctl(masterFD, terminalDarwinTIOCPTYGNAME, uintptr(unsafe.Pointer(&nameBuf[0]))); legacyErr != nil {
			cleanup()
			return nil, nil, fmt.Errorf("ptsname: %w", err)
		}
	}
	name := terminalCString(nameBuf)

	slaveFD, err := syscall.Open(name, syscall.O_RDWR|syscall.O_NOCTTY, 0o666)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return os.NewFile(uintptr(masterFD), name), os.NewFile(uintptr(slaveFD), name), nil
}

func terminalCString(buf []byte) string {
	for index, value := range buf {
		if value == 0 {
			return string(buf[:index])
		}
	}
	return string(buf)
}

// terminalSetWinsize applies the requested rows/cols to the terminal.
func terminalSetWinsize(fd uintptr, rows, cols int) {
	var size [4]uint16
	size[0] = uint16(rows)
	size[1] = uint16(cols)
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, terminalDarwinTIOCSWINSZ, uintptr(unsafe.Pointer(&size[0])))
}
