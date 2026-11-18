package platform

import (
	"os/exec"
	"runtime"
)

func OpenPath(path string) error {
	var launcher string
	switch runtime.GOOS {
	case "darwin":
		launcher = "open"
	case "windows":
		launcher = "explorer"
	default:
		launcher = "xdg-open"
	}
	command := exec.Command(launcher, path)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
