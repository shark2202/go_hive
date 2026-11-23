package server

// The `channels` section: optional channel plugins upstream starts through
// the `hive.channels` entry-point group when channels.<name>.enabled is true.
//
// The Go host (internal/channels) discovers these entries itself and starts
// one stdio subprocess per enabled one; this file keeps the field-level reader
// the rest of the server uses. A missing or non-object section reads as none,
// a non-object entry is skipped, and `enabled` keeps Python truthiness.
//
// Parity: framework/server/channels.py::start_channels (the settings read and
// the enabled gate), handle_list_channels (GET /api/channels keeps the same
// shape; with no host the list is empty, exactly as a Python process with no
// channel packages installed answers []).

import (
	"github.com/shark2202/go_hive/go-hive/internal/channels"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

// ChannelSection is one configured channel entry.
type ChannelSection struct {
	Name    string
	Enabled bool
}

// ChannelSections reads `channels` from a configuration map: a missing or
// non-object section reads as none; each object entry is one channel; only a
// truthy `enabled` counts, and truthiness is Python's (JSON true, 1 and
// "true" all count; false, 0, "" and null do not) — upstream's gate is
// `settings.get(name, {}).get("enabled")`.
//
// Parity: framework/server/channels.py::start_channels
func ChannelSections(config map[string]any) []ChannelSection {
	plugins := channels.Plugins(config)
	if len(plugins) == 0 {
		return nil
	}
	sections := make([]ChannelSection, 0, len(plugins))
	for _, plugin := range plugins {
		sections = append(sections, ChannelSection{Name: plugin.Name, Enabled: plugin.Enabled})
	}
	return sections
}

// ChannelSectionsFromHome is ChannelSections over HIVE_HOME/configuration.json.
func ChannelSectionsFromHome(home hivehome.Home) []ChannelSection {
	return ChannelSections(HiveConfig(home))
}
