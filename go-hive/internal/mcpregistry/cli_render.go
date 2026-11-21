package mcpregistry

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (c *CLI) renderInstalledTable(entries []map[string]any) {
	if len(entries) == 0 {
		fmt.Fprintln(c.out(), "No servers installed.")
		fmt.Fprintln(c.out(), "Run 'hive mcp install <name>' or 'hive mcp add' to get started.")
		return
	}

	nameW, transportW := 0, 0
	for _, entry := range entries {
		if n := runeLen(asString(entry["name"])); n > nameW {
			nameW = n
		}
		if n := runeLen(asString(entry["transport"])); n > transportW {
			transportW = n
		}
	}
	if nameW < 4 {
		nameW = 4
	}
	if transportW < 9 {
		transportW = 9
	}

	header := "  " + padRight("NAME", nameW) + "  " + padRight("TRANSPORT", transportW) +
		"  " + padRight("MODE", 5) + "  " + padRight("ENABLED", 7) + "  " + padRight("HEALTH", 9) +
		"  " + padRight("TOOLS", 5) + "  " + padRight("TRUST", 10) + "  " + "SOURCE"
	fmt.Fprintln(c.out(), header)
	fmt.Fprintln(c.out(), "  "+strings.Repeat("─", runeLen(header)-2))

	for _, entry := range entries {
		enabled := "no"
		if getTruthy(entry, "enabled", true) {
			enabled = "yes"
		}
		health := "unknown"
		if status := asString(entry["last_health_status"]); status != "" {
			health = status
		}
		symbol := healthSymbol(health)
		source := asString(entry["source"])
		manifest := asMap(entry["manifest"])
		toolsCount := toolsCountString(manifest, entry)
		trust := asString(manifest["status"])
		mode := "tools"
		if _, ok := entry["mode"]; ok {
			mode = asString(entry["mode"])
		}
		fmt.Fprintln(c.out(), "  "+padRight(asString(entry["name"]), nameW)+
			"  "+padRight(asString(entry["transport"]), transportW)+
			"  "+padRight(mode, 5)+
			"  "+padRight(enabled, 7)+
			"  "+symbol+" "+padRight(health, 7)+
			"  "+padRight(toolsCount, 5)+
			"  "+padRight(trust, 10)+
			"  "+source)
	}
}

func (c *CLI) renderAvailableTable(entries []map[string]any) {
	if len(entries) == 0 {
		fmt.Fprintln(c.out(), "No servers in registry index.")
		fmt.Fprintln(c.out(), "Run 'hive mcp update' to refresh the index.")
		return
	}

	nameW := 0
	for _, entry := range entries {
		if n := runeLen(asString(entry["name"])); n > nameW {
			nameW = n
		}
	}
	if nameW < 4 {
		nameW = 4
	}

	header := "  " + padRight("NAME", nameW) + "  " + padRight("VERSION", 9) + "  " + padRight("STATUS", 10) + "  DESCRIPTION"
	fmt.Fprintln(c.out(), header)
	fmt.Fprintln(c.out(), "  "+strings.Repeat("─", runeLen(header)-2))

	for _, entry := range entries {
		version := pyStr(entry["version"])
		status := "community"
		if _, ok := entry["status"]; ok {
			status = pyStr(entry["status"])
		}
		desc := pyStr(entry["description"])
		if runeLen(desc) > 60 {
			desc = truncateRunes(desc, 57) + "..."
		}
		fmt.Fprintln(c.out(), "  "+padRight(asString(entry["name"]), nameW)+
			"  "+padRight(version, 9)+
			"  "+padRight(status, 10)+
			"  "+desc)
	}
}

func maskOverrides(overrides map[string]any) map[string]any {
	masked := map[string]any{
		"env":     map[string]any{},
		"headers": map[string]any{},
	}
	if env := asMap(overrides["env"]); len(env) > 0 {
		bucket := map[string]any{}
		for key := range env {
			bucket[key] = "<set>"
		}
		masked["env"] = bucket
	}
	if headers := asMap(overrides["headers"]); len(headers) > 0 {
		bucket := map[string]any{}
		for key := range headers {
			bucket[key] = "<set>"
		}
		masked["headers"] = bucket
	}
	return masked
}

func (c *CLI) emitJSON(data any) {
	encoder := json.NewEncoder(c.out())
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(data)
}
