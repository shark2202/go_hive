package browserbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"
)

// ConnectionStatus is the per-profile view exposed by GET /status.
type ConnectionStatus struct {
	Label            string `json:"label"`
	ExtensionID      string `json:"extension_id"`
	Version          string `json:"version"`
	ProtocolVersion  int    `json:"protocol_version"`
	ConnectedSinceMS int64  `json:"connected_since_ms"`
	LastPongAgeMS    *int64 `json:"last_pong_age_ms"`
	InFlight         int64  `json:"in_flight"`
	IsDefault        bool   `json:"is_default"`
	Tabs             int    `json:"tabs"`
}

// Status is the GET /status payload. Field names intentionally follow the
// upstream Python bridge where they overlap so the shipped extension side
// panel can keep rendering its basic connection rail.
//
// Field set vs upstream bridge.py::status_payload: the three fields the side
// panel's footer/connection rail reads (bridge_version, runtime_version) and
// the per-listener bind_errors diagnostic are now carried (R11). The remaining
// Go additions (tabs, tabs_total, profiles, capabilities, viewport_scale and
// connections[].tabs) are documented additive extensions used by the local
// panel routes and probes; they do not replace any upstream field.
type Status struct {
	Bridge         string             `json:"bridge"`
	Port           int                `json:"port"`
	Connected      bool               `json:"connected"`
	Connections    []ConnectionStatus `json:"connections"`
	UptimeMS       int64              `json:"uptime_ms"`
	BridgeVersion  *string            `json:"bridge_version"`
	RuntimeVersion *string            `json:"runtime_version"`
	Tabs           map[string]int     `json:"tabs"`
	TabsTotal      int                `json:"tabs_total"`
	Profiles       []string           `json:"profiles"`
	Listening      ListeningStatus    `json:"listening"`
	Capabilities   []string           `json:"capabilities"`
	SystemBlocker  *Blocker           `json:"system_blocker"`
	// ControlRPCClients is the live count of control-RPC consumers, the same
	// signal the idle watchdog reads. RuntimeAlive is true/false when there is
	// evidence either way and null when no client has identified yet.
	ControlRPCClients int   `json:"control_rpc_clients"`
	RuntimeAlive      *bool `json:"runtime_alive"`
	// ViewportScale is the GCU viewport scale this bridge runs with: the
	// runtime's resolved get_gcu_viewport_scale / HIVE_BROWSER_VIEWPORT_SCALE.
	// Reported only — upstream's getter has had no live consumer since the
	// extension-based browser replaced the launched one, so capture geometry
	// is unchanged.
	ViewportScale float64 `json:"viewport_scale"`
	// BindErrors maps a listener role to its bind failure ("<addr> — <err>"),
	// mirroring upstream's _bind_errors: it lets a caller tell "listener never
	// bound (port conflict)" apart from "bound, extension idle". The fatal
	// listeners (ws/status) never appear here — a bridge that cannot bind them
	// does not start. Optional listeners report under "legacy_ws",
	// "legacy_status" and "control_rpc", the same roles upstream records.
	BindErrors map[string]string `json:"bind_errors"`
}

// ListeningStatus reports which listeners are bound.
type ListeningStatus struct {
	WS         bool `json:"ws"`
	Status     bool `json:"status"`
	ControlRPC bool `json:"control_rpc"`
	LegacyWS   bool `json:"legacy_ws"`
	// LegacyStatus reports the legacy 9229+1 status listener (upstream's
	// LEGACY_STATUS_PORT 9230), bound whenever the legacy pair is enabled.
	LegacyStatus bool `json:"legacy_status"`
}

// recordBindError stores one optional listener's bind failure for /status.
// Mirrors upstream _bind_errors writes in start(): the address and the error
// travel together so the side panel can name the conflicting port.
func (b *Bridge) recordBindError(role, addr string, err error) {
	if err == nil {
		return
	}
	b.stateMu.Lock()
	b.bindErrors[role] = fmt.Sprintf("%s — %v", addr, err)
	b.stateMu.Unlock()
}

// bindErrorSnapshot copies the bind-error map so /status never serializes a
// map a concurrent start() is still writing.
func (b *Bridge) bindErrorSnapshot() map[string]string {
	b.stateMu.Lock()
	defer b.stateMu.Unlock()
	out := make(map[string]string, len(b.bindErrors))
	for role, msg := range b.bindErrors {
		out[role] = msg
	}
	return out
}

// Status collects connection state and refreshes tab counts with a short
// live tab.list per profile. If an extension is busy, the last cached count
// is reported instead of blocking the endpoint.
func (b *Bridge) Status(ctx context.Context) Status {
	type target struct {
		label string
		conn  *Connection
	}
	b.mu.RLock()
	targets := make([]target, 0, len(b.conns))
	for label, conn := range b.conns {
		targets = append(targets, target{label: label, conn: conn})
	}
	b.mu.RUnlock()

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			qctx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
			defer cancel()
			raw, err := t.conn.Call(qctx, "tab.list", nil, 750*time.Millisecond)
			if err != nil {
				return
			}
			var res struct {
				Tabs []struct {
					ID int `json:"id"`
				} `json:"tabs"`
			}
			if json.Unmarshal(raw, &res) != nil {
				return
			}
			ids := make([]int, 0, len(res.Tabs))
			for _, tab := range res.Tabs {
				ids = append(ids, tab.ID)
			}
			b.replaceTabs(t.label, ids)
		}(t)
	}
	wg.Wait()

	defaultLabel := b.defaultLabel()
	b.serverMu.Lock()
	// Read the control field directly: ControlListening() takes serverMu.
	controlListening := b.control != nil && b.control.listening()
	listening := ListeningStatus{
		WS:           b.wss != nil,
		Status:       b.httpServer != nil,
		ControlRPC:   controlListening,
		LegacyWS:     b.legacyWSS != nil,
		LegacyStatus: b.legacyStatusSrv != nil,
	}
	wsAddr := b.wsAddr
	b.serverMu.Unlock()

	b.mu.RLock()
	now := time.Now()
	conns := make([]ConnectionStatus, 0, len(b.conns))
	profiles := make([]string, 0, len(b.conns))
	for label, conn := range b.conns {
		_, extID, version, protocol := conn.identity()
		pong := time.Unix(0, conn.lastPong.Load())
		age := now.Sub(pong).Milliseconds()
		if age < 0 {
			age = 0
		}
		agePtr := age
		conns = append(conns, ConnectionStatus{
			Label:            label,
			ExtensionID:      extID,
			Version:          version,
			ProtocolVersion:  protocol,
			ConnectedSinceMS: now.Sub(conn.connectedAt).Milliseconds(),
			LastPongAgeMS:    &agePtr,
			InFlight:         conn.inFlight.Load(),
			IsDefault:        label == defaultLabel,
			Tabs:             b.counts[label],
		})
		profiles = append(profiles, label)
	}
	b.mu.RUnlock()
	sortStrings(profiles)

	tabs, total := b.countTabs()
	bridgeVersion := b.cfg.BridgeVersion
	return Status{
		Bridge:            "running",
		Port:              portOf(wsAddr),
		Connected:         len(conns) > 0,
		Connections:       conns,
		UptimeMS:          now.Sub(b.started).Milliseconds(),
		BridgeVersion:     &bridgeVersion,
		RuntimeVersion:    b.cfg.RuntimeVersion,
		Tabs:              tabs,
		TabsTotal:         total,
		Profiles:          profiles,
		Listening:         listening,
		Capabilities:      SupportedCommands(),
		SystemBlocker:     b.SystemBlocker(),
		ControlRPCClients: b.ControlClients(),
		RuntimeAlive:      b.RuntimeAlive(),
		ViewportScale:     b.cfg.ViewportScale,
		BindErrors:        b.bindErrorSnapshot(),
	}
}

// Profiles returns the connected extension profiles.
func (b *Bridge) Profiles() []map[string]any {
	defaultLabel := b.defaultLabel()
	b.mu.RLock()
	defer b.mu.RUnlock()
	now := time.Now()
	out := make([]map[string]any, 0, len(b.conns))
	for label, conn := range b.conns {
		_, extID, version, protocol := conn.identity()
		out = append(out, map[string]any{
			"label":              label,
			"extension_id":       extID,
			"version":            version,
			"protocol_version":   protocol,
			"connected_since_ms": now.Sub(conn.connectedAt).Milliseconds(),
			"is_default":         label == defaultLabel,
			"starred":            label == b.starredDefault,
			"tabs":               b.counts[label],
		})
	}
	sortProfiles(out)
	return out
}

func portOf(addr string) int {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(port)
	return n
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func sortProfiles(v []map[string]any) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0; j-- {
			a, _ := v[j]["label"].(string)
			bb, _ := v[j-1]["label"].(string)
			if a >= bb {
				break
			}
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
