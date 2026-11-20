// Command chromeloader loads the bundled unpacked Chrome extension into a
// locally installed branded Chrome and keeps that browser alive.
//
// Why this exists: Chrome 137+ ignores --load-extension in branded builds, so a
// verification run cannot use the documented CLI switch. The supported
// mechanism is the DevTools Extensions domain (`Extensions.loadUnpacked`), which
// requires the browser to be started with --remote-debugging-pipe and
// --enable-unsafe-extension-debugging and to be driven over file descriptors 3
// (read) and 4 (write) with NUL-terminated JSON messages.
//
// This is verification tooling, not part of the bridge runtime: the bridge
// itself never talks to Chrome's own DevTools transport.
//
// Usage:
//
//	chromeloader -ext <abs path to extension dir> -profile <user data dir> \
//	             -ready-file <file> [-chrome <binary>] [-timeout 30s]
//
// On success the ready file receives one JSON line
// {"ok":true,"extensionId":"…","chromePid":N} and the process blocks until it
// receives SIGINT/SIGTERM, then kills Chrome and exits 0.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

func main() {
	ext := flag.String("ext", "", "absolute path to the unpacked extension directory")
	profile := flag.String("profile", "", "Chrome user-data-dir for the run")
	readyFile := flag.String("ready-file", "", "file to write the ready line into")
	chromePath := flag.String("chrome", defaultChromePath(), "Chrome binary")
	timeout := flag.Duration("timeout", 30*time.Second, "how long to wait for Extensions.loadUnpacked")
	urls := flag.String("open", "about:blank", "initial URL")
	flag.Parse()

	if *ext == "" || *profile == "" {
		fail("-ext and -profile are required")
	}
	extAbs, err := filepath.Abs(*ext)
	if err != nil {
		fail("resolve extension path: %v", err)
	}
	if info, err := os.Stat(filepath.Join(extAbs, "manifest.json")); err != nil || info.IsDir() {
		fail("no manifest.json under %s", extAbs)
	}
	if err := os.MkdirAll(*profile, 0o755); err != nil {
		fail("create profile dir: %v", err)
	}

	childRead, parentWrite, err := os.Pipe()
	if err != nil {
		fail("pipe: %v", err)
	}
	parentRead, childWrite, err := os.Pipe()
	if err != nil {
		fail("pipe: %v", err)
	}

	cmd := exec.Command(*chromePath,
		"--user-data-dir="+*profile,
		"--remote-debugging-pipe",
		"--enable-unsafe-extension-debugging",
		"--no-first-run", "--no-default-browser-check",
		"--no-service-autorun", "--disable-sync",
		"--disable-background-networking", "--disable-component-update",
		"--disable-features=OptimizationHints,MediaRouter",
		"--window-size=1280,900",
		*urls,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// ExtraFiles become fd 3 and fd 4 in the child, exactly what the DevTools
	// pipe transport expects (read on 3, write on 4).
	cmd.ExtraFiles = []*os.File{childRead, childWrite}
	if err := cmd.Start(); err != nil {
		fail("start chrome: %v", err)
	}
	_ = childRead.Close()
	_ = childWrite.Close()

	send := func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		data = append(data, 0) // NUL-terminated frames
		_, err = parentWrite.Write(data)
		return err
	}

	// Chrome is chatty on this transport; drain it in the background so the
	// pipe never fills while we wait for our own responses.
	var mu sync.Mutex
	loaded := make(chan map[string]any, 1)
	go func() {
		reader := newFrameReader(parentRead)
		for {
			frame, err := reader.next()
			if err != nil {
				return
			}
			var msg map[string]any
			if json.Unmarshal([]byte(frame), &msg) != nil {
				continue
			}
			if id, _ := msg["id"].(float64); int(id) == loadRequestID {
				mu.Lock()
				select {
				case loaded <- msg:
				default:
				}
				mu.Unlock()
			} else if method, _ := msg["method"].(string); method != "" {
				fmt.Fprintf(os.Stderr, "chromeloader: event %s\n", method)
			}
		}
	}()

	go func() {
		time.Sleep(time.Second)
		_ = send(map[string]any{"id": 1, "method": "Browser.getVersion"})
		_ = send(map[string]any{
			"id":     loadRequestID,
			"method": "Extensions.loadUnpacked",
			"params": map[string]any{"path": extAbs},
		})
	}()

	var result map[string]any
	select {
	case result = <-loaded:
	case <-time.After(*timeout):
		_ = cmd.Process.Kill()
		fail("timed out waiting for Extensions.loadUnpacked after %s", *timeout)
	}

	payload, _ := result["result"].(map[string]any)
	if errMsg, ok := result["error"].(map[string]any); ok {
		_ = cmd.Process.Kill()
		fail("Extensions.loadUnpacked failed: %v", errMsg["message"])
	}
	extID, _ := payload["id"].(string)
	if extID == "" {
		_ = cmd.Process.Kill()
		fail("Extensions.loadUnpacked returned no extension id: %v", result)
	}

	ready := map[string]any{"ok": true, "extensionId": extID, "chromePid": cmd.Process.Pid}
	if *readyFile != "" {
		line, _ := json.Marshal(ready)
		if err := os.WriteFile(*readyFile, append(line, '\n'), 0o600); err != nil {
			fail("write ready file: %v", err)
		}
	}
	fmt.Printf("chromeloader: loaded extension %s (chrome pid %d)\n", extID, cmd.Process.Pid)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case <-stop:
	case <-waitChrome(cmd):
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	fmt.Println("chromeloader: chrome stopped")
}

const loadRequestID = 2

type frameReader struct {
	r   io.Reader
	buf []byte
}

func newFrameReader(r io.Reader) *frameReader { return &frameReader{r: r} }

// next returns the next NUL-terminated frame.
func (f *frameReader) next() (string, error) {
	for {
		for i, b := range f.buf {
			if b == 0 {
				frame := string(f.buf[:i])
				f.buf = f.buf[i+1:]
				return frame, nil
			}
		}
		tmp := make([]byte, 8192)
		n, err := f.r.Read(tmp)
		if n > 0 {
			f.buf = append(f.buf, tmp[:n]...)
			continue
		}
		if err != nil {
			return "", err
		}
	}
}

func waitChrome(cmd *exec.Cmd) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	return done
}

func defaultChromePath() string {
	for _, candidate := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "google-chrome"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "chromeloader: "+format+"\n", args...)
	os.Exit(1)
}
