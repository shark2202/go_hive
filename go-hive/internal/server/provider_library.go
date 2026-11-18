package server

// Parity: framework/server/routes_config.py::_write_role_section

import (
	"os"
	"strings"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

var RoleSections = []string{"llm", "worker_llm", "vision_fallback", "judge"}

func isRoleSection(role string) bool {
	for _, candidate := range RoleSections {
		if role == candidate {
			return true
		}
	}
	return false
}

func pyStrOrEmpty(value any) string {
	if !pyTruthy(value) {
		return ""
	}
	return pyStr(value)
}

type ProviderKeyValidator func(provider, apiKey, apiBase, model string) (valid any, message string)

type RoleWriteDeps struct {
	Validate ProviderKeyValidator
	HotSwap  func(fullModel, apiKey, apiBase string) int
}

func validateFlag(body map[string]any) bool {
	value, present := body["validate"]
	if !present {
		return true
	}
	return pyTruthy(value)
}

// Parity: framework/server/routes_config.py::handle_get_llm_sections
func GetLLMSections(config map[string]any) map[string]any {
	sections := map[string]any{}
	for _, role := range RoleSections {
		if value, ok := config[role]; ok && pyTruthy(value) {
			sections[role] = value
		} else {
			sections[role] = nil
		}
	}
	return sections
}

func LLMSectionsResponse(home hivehome.Home) map[string]any {
	return GetLLMSections(HiveConfig(home))
}

// Parity: framework/server/routes_config.py::_get_provider_library
func GetProviderLibrary(config map[string]any) map[string]any {
	library := map[string]any{}
	raw, ok := config["provider_library"].(map[string]any)
	if !ok {
		return library
	}
	for name, value := range raw {
		if section, ok := value.(map[string]any); ok {
			library[pyStr(name)] = section
		}
	}
	return library
}

func ProviderLibraryResponse(home hivehome.Home) map[string]any {
	return map[string]any{"library": GetProviderLibrary(HiveConfig(home))}
}

// Parity: framework/server/routes_config.py::handle_put_provider_library
func PutProviderLibrary(home hivehome.Home, body map[string]any) (int, map[string]any) {
	name := strings.TrimSpace(pyStrOrEmpty(body["name"]))
	if name == "" {
		return 400, map[string]any{"error": "'name' is required"}
	}

	if sectionValue, present := body["section"]; !present || sectionValue == nil {
		err := MutateHiveConfig(home, func(config map[string]any) bool {
			library, ok := config["provider_library"].(map[string]any)
			if !ok {
				library = map[string]any{}
			}
			delete(library, name)
			if len(library) > 0 {
				config["provider_library"] = library
			} else {
				delete(config, "provider_library")
			}
			return true
		})
		if err != nil {
			return 500, map[string]any{"error": internalServerError}
		}
		return 200, map[string]any{"name": name, "section": nil}
	}

	section, ok := body["section"].(map[string]any)
	if !ok {
		return 400, map[string]any{"error": "'section' must be a JSON object or null"}
	}
	if strings.TrimSpace(pyStrOrEmpty(section["model"])) == "" {
		return 400, map[string]any{"error": "'model' is required"}
	}

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		library, ok := config["provider_library"].(map[string]any)
		if !ok {
			library = map[string]any{}
		}
		library[name] = section
		config["provider_library"] = library
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}
	return 200, map[string]any{"name": name, "section": section}
}

// Parity: framework/server/routes_config.py::_write_role_section
func WriteRoleSection(home hivehome.Home, role string, section map[string]any, validate bool, deps RoleWriteDeps) (int, map[string]any) {
	model := strings.TrimSpace(pyStrOrEmpty(section["model"]))
	if model == "" {
		return 400, map[string]any{"error": "'model' is required"}
	}

	provider := pyStrOrEmpty(section["provider"])
	if provider == "" {
		provider = "openai"
	}
	apiBase := strings.TrimSpace(pyStrOrEmpty(section["api_base"]))
	apiKey := pyStrOrEmpty(section["api_key"])
	if apiKey == "" {
		if envVar := pyStrOrEmpty(section["api_key_env_var"]); envVar != "" {
			apiKey = os.Getenv(envVar)
		}
	}

	if validate && apiKey != "" && apiBase != "" && role != "judge" && deps.Validate != nil {
		valid, message := deps.Validate(provider, apiKey, apiBase, model)
		if isFalse, ok := valid.(bool); ok && !isFalse {
			if message == "" {
				message = "unknown error"
			}
			return 400, map[string]any{"error": "Key check failed against " + apiBase + ": " + message}
		}
	}

	err := MutateHiveConfig(home, func(config map[string]any) bool {
		config[role] = section
		return true
	})
	if err != nil {
		return 500, map[string]any{"error": internalServerError}
	}

	swapped := 0
	if role == "llm" && deps.HotSwap != nil {
		swapped = deps.HotSwap(provider+"/"+pyStr(section["model"]), apiKey, apiBase)
	}
	return 200, map[string]any{"role": role, "section": section, "sessions_swapped": swapped}
}

// Parity: framework/server/routes_config.py::handle_put_llm_section
func PutLLMSection(home hivehome.Home, body map[string]any, deps RoleWriteDeps) (int, map[string]any) {
	role := pyStrOrEmpty(body["role"])
	if !isRoleSection(role) {
		return 400, map[string]any{"error": "'role' must be llm | worker_llm | vision_fallback | judge"}
	}

	sectionValue, present := body["section"]
	if !present || sectionValue == nil {
		if role == "llm" {
			return 400, map[string]any{"error": "The llm slot cannot be cleared - the runtime always needs a main model."}
		}
		err := MutateHiveConfig(home, func(config map[string]any) bool {
			if _, ok := config[role]; !ok {
				return false
			}
			delete(config, role)
			return true
		})
		if err != nil {
			return 500, map[string]any{"error": internalServerError}
		}
		return 200, map[string]any{"role": role, "section": nil}
	}

	section, ok := sectionValue.(map[string]any)
	if !ok {
		return 400, map[string]any{"error": "'section' must be a JSON object or null"}
	}
	return WriteRoleSection(home, role, section, validateFlag(body), deps)
}

// Parity: framework/server/routes_config.py::handle_apply_provider_library
func ApplyProviderLibrary(home hivehome.Home, body map[string]any, deps RoleWriteDeps) (int, map[string]any) {
	role := pyStrOrEmpty(body["role"])
	if !isRoleSection(role) {
		return 400, map[string]any{"error": "'role' must be llm | worker_llm | vision_fallback | judge"}
	}
	name := strings.TrimSpace(pyStrOrEmpty(body["name"]))
	section, ok := GetProviderLibrary(HiveConfig(home))[name].(map[string]any)
	if !ok {
		return 404, map[string]any{"error": "No provider library entry named '" + name + "'"}
	}
	return WriteRoleSection(home, role, section, validateFlag(body), deps)
}
