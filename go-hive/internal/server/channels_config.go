package server

// The `channels` section: optional channel packages upstream starts through
// the `hive.channels` entry-point group when channels.<name>.enabled is true.
//
// The Go port has no channel plugin host: it cannot discover entry points or
// run a channel's lifecycle, so an enabled flag has nothing to start. The
// section is still read the way upstream reads it (an object, each value an
// object, `enabled` truthy), so the field has a real reader and a serve-time
// notice can say plainly that a configured channel will not run — instead of
// silently pretending the flag took effect.
//
// Parity: framework/server/channels.py::start_channels (the settings read and
// the enabled gate), handle_list_channels (GET /api/channels keeps the same
// shape; with no host there are no installed channels to list, exactly as a
// Python process with no channel packages installed answers []).

import (
	"sort"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
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
	raw, ok := config["channels"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	sort.Strings(names)

	sections := make([]ChannelSection, 0, len(names))
	for _, name := range names {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			continue
		}
		sections = append(sections, ChannelSection{
			Name:    name,
			Enabled: pyTruthy(entry["enabled"]),
		})
	}
	return sections
}

// ChannelSectionsFromHome is ChannelSections over HIVE_HOME/configuration.json.
func ChannelSectionsFromHome(home hivehome.Home) []ChannelSection {
	return ChannelSections(HiveConfig(home))
}

// UnhostedChannels lists the configured channels that ask to run. Each one is
// a configuration the Go runtime reads but cannot act on; the serve startup
// prints one line per name so the operator learns the flag is inert here.
func UnhostedChannels(home hivehome.Home) []string {
	sections := ChannelSectionsFromHome(home)
	names := make([]string, 0, len(sections))
	for _, section := range sections {
		if section.Enabled && strings.TrimSpace(section.Name) != "" {
			names = append(names, section.Name)
		}
	}
	return names
}
