package colony

// Colony worker profiles: the per-spawn template binding the runtime applies
// before a worker starts.
//
// Upstream keeps a colony's profiles in metadata.json (`worker_profiles`) and
// resolves the dispatched profile in ColonyRuntime.spawn: the resolved
// `integrations` map becomes the worker's default account aliases and the
// resolved `browser_profile` becomes the Chrome profile its browser tools and
// tab-group reap target. The Go port used to only stamp ProfileName on the
// worker; this file reads the same on-disk metadata (the colony's storage
// path is the colony directory, so `metadata.json` lives there) and returns
// the binding the spawn path applies.
//
// Parity: framework/host/worker_profiles.py::list_worker_profiles
// Parity: framework/host/worker_profiles.py::get_worker_profile
// Parity: framework/host/colony_runtime.py::ColonyRuntime.spawn (profile
// resolution: _resolved_profile / _profile_integrations / _profile_browser)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const defaultWorkerProfileName = "default"

// workerProfileNameRE mirrors worker_profiles._PROFILE_NAME_RE: lowercase
// alphanumeric leading character, then lowercase alphanumerics, `_` or `-`,
// at most 64 characters.
var workerProfileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// WorkerProfile is the spawn-relevant slice of a colony worker profile.
// Task/skill/prompt fields are template concerns handled elsewhere; the
// runtime only needs the credential aliases and the browser binding.
type WorkerProfile struct {
	Name           string
	Integrations   map[string]string
	BrowserProfile string
}

func validWorkerProfileName(name string) bool {
	return workerProfileNameRE.MatchString(name)
}

// workerProfiles reads the colony's declared profiles from metadata.json.
// Missing/unreadable metadata and an empty declaration both fall back to the
// synthetic default profile, exactly like list_worker_profiles: dispatch logic
// can then treat the registry as always non-empty.
func (r *ColonyRuntime) workerProfiles() []WorkerProfile {
	defaults := []WorkerProfile{{Name: defaultWorkerProfileName, Integrations: map[string]string{}}}
	if r == nil || r.storagePath == "" {
		return defaults
	}
	data, err := os.ReadFile(filepath.Join(r.storagePath, colonyMetadataFile))
	if err != nil {
		return defaults
	}
	metadata := map[string]any{}
	if json.Unmarshal(data, &metadata) != nil {
		return defaults
	}
	raw, ok := metadata["worker_profiles"].([]any)
	if !ok || len(raw) == 0 {
		return defaults
	}
	profiles := make([]WorkerProfile, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		name := strings.TrimSpace(pythonStringValue(object["name"]))
		if name == "" {
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		if !validWorkerProfileName(name) {
			continue
		}
		seen[name] = struct{}{}
		profile := WorkerProfile{Name: name, Integrations: map[string]string{}}
		if integrations, ok := object["integrations"].(map[string]any); ok {
			for key, value := range integrations {
				provider := strings.TrimSpace(pythonStringValue(key))
				alias := strings.TrimSpace(pythonStringValue(value))
				if provider != "" && alias != "" {
					profile.Integrations[provider] = alias
				}
			}
		}
		profile.BrowserProfile = strings.TrimSpace(pythonStringValue(object["browser_profile"]))
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return defaults
	}
	return profiles
}

// getWorkerProfile returns one declared profile by name, or false when the
// colony did not declare it (upstream's get_worker_profile returns None and
// the spawn keeps the caller's raw profile name with no binding).
func (r *ColonyRuntime) getWorkerProfile(name string) (WorkerProfile, bool) {
	if name == "" {
		return WorkerProfile{}, false
	}
	for _, profile := range r.workerProfiles() {
		if profile.Name == name {
			return profile, true
		}
	}
	return WorkerProfile{}, false
}

// resolveWorkerProfileBinding mirrors the spawn-side resolution:
//   - profileName empty -> no lookup, resolved name "", no aliases,
//     browser "default" (an unbound/legacy worker);
//   - declared profile -> its name, a copy of its integrations, and its
//     browser_profile (empty -> "default");
//   - undeclared profile -> the raw name is kept as the label, with no
//     aliases and the default browser profile (upstream silently falls back).
func (r *ColonyRuntime) resolveWorkerProfileBinding(profileName string) (string, map[string]string, string) {
	name := strings.TrimSpace(profileName)
	if name == "" {
		return "", map[string]string{}, defaultBrowserProfile
	}
	integrations := map[string]string{}
	browserProfile := defaultBrowserProfile
	if profile, ok := r.getWorkerProfile(name); ok {
		name = profile.Name
		for provider, alias := range profile.Integrations {
			integrations[provider] = alias
		}
		if strings.TrimSpace(profile.BrowserProfile) != "" {
			browserProfile = strings.TrimSpace(profile.BrowserProfile)
		}
	}
	return name, integrations, browserProfile
}

// pythonStringValue renders metadata scalars the way Python's str() would for
// the profile fields: strings stay as-is, other scalars use their JSON form.
func pythonStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "True"
		}
		return "False"
	default:
		return ""
	}
}
