//go:build linux

package builtin

import (
	"fmt"
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

// Linux tty ioctls: unlock the pty master, read its slave number, set the
// window size.
const (
	terminalLinuxTIOCSPTLCK = 0x40045431
	terminalLinuxTIOCGPTN   = 0x80045430
	terminalLinuxTIOCSWINSZ = 0x5414
)

func terminalLinuxIoctl(fd int, request, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

// terminalOpenPTY returns the master and slave ends of a fresh pseudo-terminal.
func terminalOpenPTY() (*os.File, *os.File, error) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0o666)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	cleanup := func() {
		_ = syscall.Close(masterFD)
	}
	var unlock int32
	if err := terminalLinuxIoctl(masterFD, terminalLinuxTIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("unlockpt: %w", err)
	}
	var number uint32
	if err := terminalLinuxIoctl(masterFD, terminalLinuxTIOCGPTN, uintptr(unsafe.Pointer(&number))); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("ptsname: %w", err)
	}
	name := "/dev/pts/" + strconv.FormatUint(uint64(number), 10)
	slaveFD, err := syscall.Open(name, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0o666)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return os.NewFile(uintptr(masterFD), name), os.NewFile(uintptr(slaveFD), name), nil
}

// terminalSetWinsize applies the requested rows/cols to the terminal.
func terminalSetWinsize(fd uintptr, rows, cols int) {
	var size [4]uint16
	size[0] = uint16(rows)
	size[1] = uint16(cols)
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, terminalLinuxTIOCSWINSZ, uintptr(unsafe.Pointer(&size[0])))
}
