package lsp

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ServerStatus is the status row for one configured server.
type ServerStatus struct {
	Name        string
	Source      string
	Command     string
	CommandPath string
	RootMarker  string
	Rooted      bool
	Disabled    bool
	Available   bool
	Reason      string
	FileTypes   []string
	LanguageID  string
	Linter      bool
	Running     bool
	IdleFor     time.Duration
	ServerName  string
	ServerVer   string
	LastError   string
	FailedUntil time.Time
}

// StatusInput is everything the status renderer needs.
type StatusInput struct {
	Settings   Settings
	Idle       time.Duration
	Sources    []string
	Servers    []ServerStatus
	Catalog    int
	CatalogMax int
	Warnings   []string
	Now        time.Time
}

// renderStatus renders deterministic status text and a structured map.
func renderStatus(in StatusInput) (string, map[string]any) {
	available := 0
	running := 0
	for _, server := range in.Servers {
		if server.Available {
			available++
		}
		if server.Running {
			running++
		}
	}
	var b strings.Builder
	b.WriteString("lsp status\n")
	fmt.Fprintf(&b, "catalog: %d embedded (target %d)\n", in.Catalog, in.CatalogMax)
	fmt.Fprintf(&b, "settings: enabled=%t lazy=%t requestTimeout=%ds shared=%t diagnosticsOnWrite=%t diagnosticsOnEdit=%t formatOnWrite=%t\n",
		in.Settings.Enabled, in.Settings.Lazy, in.Settings.RequestTimeout, in.Settings.Shared,
		in.Settings.DiagnosticsOnWrite, in.Settings.DiagnosticsOnEdit, in.Settings.FormatOnWrite)
	fmt.Fprintf(&b, "idleTimeout: %s\n", in.Idle)
	fmt.Fprintf(&b, "config: %s\n", strings.Join(in.Sources, ", "))
	fmt.Fprintf(&b, "servers: %d configured, %d available, %d running\n", len(in.Servers), available, running)
	for _, server := range in.Servers {
		state := "available"
		switch {
		case server.Disabled:
			state = "disabled"
		case server.Running:
			state = "running"
			if server.IdleFor > 0 {
				state = fmt.Sprintf("running (idle %s)", server.IdleFor.Round(time.Second))
			}
		case !server.Rooted:
			state = "unrooted"
		case !server.Available:
			state = "missing"
		}
		if !server.FailedUntil.IsZero() && server.FailedUntil.After(in.Now) {
			state = fmt.Sprintf("failed (backoff %s)", server.FailedUntil.Sub(in.Now).Round(time.Second))
		}
		fmt.Fprintf(&b, "  %s  %s", server.Name, state)
		if server.CommandPath != "" {
			fmt.Fprintf(&b, "  %s", server.CommandPath)
		} else if server.Command != "" {
			fmt.Fprintf(&b, "  command=%s", server.Command)
		}
		if server.RootMarker != "" {
			fmt.Fprintf(&b, "  root=%s", server.RootMarker)
		}
		if server.Running && server.ServerName != "" {
			fmt.Fprintf(&b, "  server=%s", server.ServerName)
			if server.ServerVer != "" {
				fmt.Fprintf(&b, " %s", server.ServerVer)
			}
		}
		if server.Linter {
			b.WriteString("  [linter]")
		}
		if server.Reason != "" {
			fmt.Fprintf(&b, "  reason=%s", server.Reason)
		}
		if server.LastError != "" {
			fmt.Fprintf(&b, "  lastError=%s", server.LastError)
		}
		b.WriteByte('\n')
	}
	if len(in.Warnings) > 0 {
		b.WriteString("warnings:\n")
		for _, warning := range in.Warnings {
			fmt.Fprintf(&b, "  - %s\n", warning)
		}
	}
	data := map[string]any{
		"action":          "status",
		"enabled":         in.Settings.Enabled,
		"catalog":         in.Catalog,
		"target":          in.CatalogMax,
		"sources":         in.Sources,
		"servers":         statusData(in.Servers),
		"warnings":        in.Warnings,
		"settings":        settingsData(in.Settings),
		"idle_timeout_ms": in.Idle.Milliseconds(),
	}
	return strings.TrimRight(b.String(), "\n"), data
}

func settingsData(settings Settings) map[string]any {
	return map[string]any{
		"enabled":            settings.Enabled,
		"lazy":               settings.Lazy,
		"requestTimeout":     settings.RequestTimeout,
		"formatOnWrite":      settings.FormatOnWrite,
		"diagnosticsOnWrite": settings.DiagnosticsOnWrite,
		"diagnosticsOnEdit":  settings.DiagnosticsOnEdit,
		"shared":             settings.Shared,
	}
}

func statusData(servers []ServerStatus) []map[string]any {
	out := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		item := map[string]any{
			"name":      server.Name,
			"available": server.Available,
			"running":   server.Running,
			"disabled":  server.Disabled,
			"rooted":    server.Rooted,
			"fileTypes": server.FileTypes,
			"source":    server.Source,
		}
		if server.CommandPath != "" {
			item["command_path"] = server.CommandPath
		} else {
			item["command"] = server.Command
		}
		if server.RootMarker != "" {
			item["root_marker"] = server.RootMarker
		}
		if server.Reason != "" {
			item["reason"] = server.Reason
		}
		if server.LastError != "" {
			item["last_error"] = server.LastError
		}
		out = append(out, item)
	}
	return out
}

// CapabilityView is the negotiated capability summary for one server.
type CapabilityView struct {
	Name          string
	Started       bool
	ServerName    string
	ServerVersion string
	SyncKind      int
	Providers     []string
	FileTypes     []string
	State         string
	Error         string
}

var (
	capabilityKeys = []struct {
		Key   string
		Label string
	}{
		{"definitionProvider", "definition"},
		{"typeDefinitionProvider", "typeDefinition"},
		{"implementationProvider", "implementation"},
		{"referencesProvider", "references"},
		{"hoverProvider", "hover"},
		{"documentSymbolProvider", "documentSymbol"},
		{"workspaceSymbolProvider", "workspaceSymbol"},
		{"diagnosticProvider", "diagnosticPull"},
		{"renameProvider", "rename"},
		{"codeActionProvider", "codeAction"},
	}
)

// capabilityProviders returns the labels the capability object supports, in
// the frozen order.
func capabilityProviders(caps ServerCapabilities) []string {
	var out []string
	for _, entry := range capabilityKeys {
		if caps.Has(entry.Key) {
			out = append(out, entry.Label)
		}
	}
	return out
}

// renderCapabilities renders one server or the configured overview.
func renderCapabilities(views []CapabilityView) (string, map[string]any) {
	data := map[string]any{"action": "capabilities"}
	if len(views) == 0 {
		return "capabilities: no servers configured", data
	}
	data["servers"] = capabilitiesData(views)
	if len(views) == 1 {
		view := views[0]
		var b strings.Builder
		fmt.Fprintf(&b, "capabilities [%s]", view.Name)
		if view.Started {
			if view.ServerName != "" {
				fmt.Fprintf(&b, " %s", view.ServerName)
			}
			if view.ServerVersion != "" {
				fmt.Fprintf(&b, " %s", view.ServerVersion)
			}
		} else {
			b.WriteString(" (not started)")
		}
		b.WriteByte('\n')
		if view.Error != "" {
			fmt.Fprintf(&b, "  error: %s\n", view.Error)
		}
		if view.Started {
			fmt.Fprintf(&b, "  textDocumentSync: %s\n", syncKindLabel(view.SyncKind))
			providers := map[string]struct{}{}
			for _, provider := range view.Providers {
				providers[provider] = struct{}{}
			}
			for _, entry := range capabilityKeys {
				mark := "no"
				if _, ok := providers[entry.Label]; ok {
					mark = "yes"
				}
				fmt.Fprintf(&b, "  %s: %s\n", entry.Label, mark)
			}
		}
		if len(view.FileTypes) > 0 {
			fmt.Fprintf(&b, "  fileTypes: %s\n", strings.Join(view.FileTypes, ", "))
		}
		return strings.TrimRight(b.String(), "\n"), data
	}
	var b strings.Builder
	fmt.Fprintf(&b, "capabilities (%d server(s))\n", len(views))
	for _, view := range views {
		if !view.Started {
			state := view.State
			if state == "" {
				state = "not started"
			}
			fmt.Fprintf(&b, "  %s: %s\n", view.Name, state)
			continue
		}
		providers := append([]string(nil), view.Providers...)
		sort.Strings(providers)
		fmt.Fprintf(&b, "  %s: %s\n", view.Name, strings.Join(providers, ", "))
	}
	return strings.TrimRight(b.String(), "\n"), data
}

func capabilitiesData(views []CapabilityView) []map[string]any {
	out := make([]map[string]any, 0, len(views))
	for _, view := range views {
		out = append(out, map[string]any{
			"name":      view.Name,
			"started":   view.Started,
			"server":    view.ServerName,
			"version":   view.ServerVersion,
			"sync":      syncKindLabel(view.SyncKind),
			"providers": view.Providers,
			"fileTypes": view.FileTypes,
			"state":     view.State,
			"error":     view.Error,
		})
	}
	return out
}

func syncKindLabel(kind int) string {
	switch kind {
	case 1:
		return "full"
	case 2:
		return "incremental"
	case 0:
		return "none"
	default:
		return fmt.Sprintf("unknown(%d)", kind)
	}
}
