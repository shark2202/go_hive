package colony

// Browser-profile binding for spawned workers.
//
// Upstream's Worker.run pre-flights a non-default bound Chrome profile: when
// the profile's Hive extension is not connected, the worker is failed before
// its first model call with a "Not attempted: …" report instead of burning
// its whole timeout on browser tools that error every call. The bound label
// also routes the worker's tab-group reap.
//
// The probe mirrors framework/host/worker.py::_connected_browser_labels: ask
// the local browser bridge's status port (HIVE_BRIDGE_PORT + 1, then the
// legacy 9230) for GET /profiles and read the profile labels. A probe failure
// is "unknown" — the caller proceeds rather than false-failing the worker.
//
// Parity: framework/host/worker.py::_connected_browser_labels
// Parity: framework/host/worker.py::Worker.run (browser-profile pre-flight)

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBrowserBridgePort = 14829
	legacyBrowserStatusPort  = 9230
	browserProbeDialTimeout  = 500 * time.Millisecond
	browserProbeReadTimeout  = time.Second
	browserProbeMaxBody      = 64 * 1024
)

func connectedBrowserProfileLabels() ([]string, error) {
	port := defaultBrowserBridgePort
	if raw := strings.TrimSpace(os.Getenv("HIVE_BRIDGE_PORT")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed < 65535 {
			port = parsed
		}
	}
	ports := []int{port + 1, legacyBrowserStatusPort}
	var lastErr error
	for _, statusPort := range ports {
		labels, err := probeBrowserProfiles(statusPort)
		if err == nil {
			return labels, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("browser bridge not reachable")
	}
	return nil, lastErr
}

func probeBrowserProfiles(port int) ([]string, error) {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", address, browserProbeDialTimeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(browserProbeReadTimeout))
	if _, err := io.WriteString(conn, "GET /profiles HTTP/1.0\r\nHost: 127.0.0.1\r\n\r\n"); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(conn, browserProbeMaxBody))
	if err != nil {
		return nil, err
	}
	separator := []byte("\r\n\r\n")
	index := strings.Index(string(raw), string(separator))
	if index < 0 {
		return nil, fmt.Errorf("browser bridge response has no header terminator")
	}
	body := raw[index+len(separator):]
	var payload struct {
		Profiles []struct {
			Label string `json:"label"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	labels := make([]string, 0, len(payload.Profiles))
	for _, profile := range payload.Profiles {
		if label := strings.TrimSpace(profile.Label); label != "" {
			labels = append(labels, label)
		}
	}
	sort.Strings(labels)
	return labels, nil
}

// workerBrowserProfileUnavailable runs the pre-flight for one worker. It
// returns ok=true when the worker must not be attempted, with the error and
// summary upstream's report carries. default/unbound workers never probe.
func (r *ColonyRuntime) workerBrowserProfileUnavailable(worker *Worker) (ok bool, errorText, summary string) {
	if worker == nil {
		return false, "", ""
	}
	profile := strings.TrimSpace(worker.BrowserProfile)
	if profile == "" || profile == defaultBrowserProfile {
		return false, "", ""
	}
	probe := r.connectedBrowserProfiles
	if probe == nil {
		probe = connectedBrowserProfileLabels
	}
	labels, err := probe()
	if err != nil {
		// Unknown (probe miss) — proceed and let the tool layer fail fast.
		return false, "", ""
	}
	connected := make([]string, 0, len(labels))
	available := false
	for _, label := range labels {
		trimmed := strings.TrimSpace(label)
		if trimmed == "" {
			continue
		}
		connected = append(connected, trimmed)
		if trimmed == profile {
			available = true
		}
	}
	if available {
		return false, "", ""
	}
	sort.Strings(connected)
	availableText := "none"
	if len(connected) > 0 {
		availableText = strings.Join(connected, ", ")
	}
	summary = fmt.Sprintf(
		"Not attempted: the Chrome profile '%s' this worker is bound to has "+
			"no connected Hive extension. Open that Chrome profile, enable the Hive "+
			"Browser Bridge extension, and label it '%s' in the side panel, then "+
			"re-dispatch. Connected browser profiles right now: %s.",
		profile, profile, availableText,
	)
	return true, fmt.Sprintf("browser profile '%s' not connected", profile), summary
}

// WorkerBrowserProfile exposes the Chrome profile label a spawned worker is
// bound to (the browser reaper and status surfaces use it). Resolves through
// the same spawn binding so a resumed worker keeps its profile.
func (r *ColonyRuntime) WorkerBrowserProfile(workerID string) string {
	worker := r.GetWorker(workerID)
	if worker == nil {
		return defaultBrowserProfile
	}
	if profile := strings.TrimSpace(worker.BrowserProfile); profile != "" {
		return profile
	}
	return defaultBrowserProfile
}
