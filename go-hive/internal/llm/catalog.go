package llm

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed model_catalog.json
var embeddedModelCatalog []byte

type ModelCatalogError struct {
	Message string
}

func (e *ModelCatalogError) Error() string { return e.Message }

func catalogErrorf(format string, args ...any) error {
	return &ModelCatalogError{Message: fmt.Sprintf(format, args...)}
}

type ModelEntry struct {
	ID                   string
	Label                string
	Recommended          bool
	MaxTokens            int
	MaxContextTokens     int
	SupportsVision       bool
	SupportsVisionSet    bool
	PricingUSDPerMTok    map[string]float64
	HasPricingUSDPerMTok bool
}

func (m ModelEntry) Clone() ModelEntry {
	out := m
	if m.PricingUSDPerMTok != nil {
		out.PricingUSDPerMTok = make(map[string]float64, len(m.PricingUSDPerMTok))
		for k, v := range m.PricingUSDPerMTok {
			out.PricingUSDPerMTok[k] = v
		}
	}
	return out
}

func (m ModelEntry) ToMap() map[string]any {
	out := map[string]any{
		"id":                 m.ID,
		"label":              m.Label,
		"recommended":        m.Recommended,
		"max_tokens":         m.MaxTokens,
		"max_context_tokens": m.MaxContextTokens,
	}
	if m.SupportsVisionSet {
		out["supports_vision"] = m.SupportsVision
	}
	if m.HasPricingUSDPerMTok {
		pricing := make(map[string]any, len(m.PricingUSDPerMTok))
		for key, rate := range m.PricingUSDPerMTok {
			pricing[key] = rate
		}
		out["pricing_usd_per_mtok"] = pricing
	}
	return out
}

type ProviderInfo struct {
	DefaultModel string
	Models       []ModelEntry
}

type ModelChoice struct {
	ID          string
	Label       string
	Recommended bool
}

type Preset struct {
	Provider         string
	Model            string
	HasModel         bool
	APIBase          string
	HasAPIBase       bool
	APIKeyEnvVar     string
	HasAPIKeyEnvVar  bool
	MaxTokens        int
	MaxContextTokens int
	ModelChoices     []ModelChoice
	HasModelChoices  bool
}

type Catalog struct {
	ProviderOrder []string
	Providers     map[string]*ProviderInfo
	PresetOrder   []string
	Presets       map[string]*Preset
}

func requireMapping(value any, path string) (map[string]any, error) {
	m, ok := value.(map[string]any)
	if !ok {
		return nil, catalogErrorf("%s must be an object", path)
	}
	return m, nil
}

func requireList(value any, path string) ([]any, error) {
	l, ok := value.([]any)
	if !ok {
		return nil, catalogErrorf("%s must be an array", path)
	}
	return l, nil
}

func isNonEmptyString(value any) bool {
	s, ok := value.(string)
	return ok && strings.TrimSpace(s) != ""
}

func positiveInt(value any) (int, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	s := n.String()
	if strings.ContainsAny(s, ".eE") {
		return 0, false
	}
	i, err := strconv.ParseInt(s, 10, 64)
	if err != nil || i <= 0 {
		return 0, false
	}
	return int(i), true
}

func isBool(value any) bool {
	_, ok := value.(bool)
	return ok
}

var pricingKeys = map[string]struct{}{
	"input":          {},
	"output":         {},
	"cache_read":     {},
	"cache_creation": {},
}

func validatePricing(value any, path string) error {
	pricing, err := requireMapping(value, path)
	if err != nil {
		return err
	}
	for _, key := range []string{"input", "output"} {
		if _, ok := pricing[key]; !ok {
			return catalogErrorf("%s.%s is required", path, key)
		}
	}
	keys := make([]string, 0, len(pricing))
	for key := range pricing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rate := pricing[key]
		if _, ok := pricingKeys[key]; !ok {
			return catalogErrorf("%s.%s is not a recognized pricing field", path, key)
		}
		num, ok := rate.(json.Number)
		if !ok {
			return catalogErrorf("%s.%s must be a non-negative number", path, key)
		}
		f, err := num.Float64()
		if err != nil || f < 0 {
			return catalogErrorf("%s.%s must be a non-negative number", path, key)
		}
	}
	return nil
}

func validateModelCatalog(data map[string]any) error {
	providersValue, ok := data["providers"]
	if !ok {
		providersValue = nil
	}
	providers, err := requireMapping(providersValue, "providers")
	if err != nil {
		return err
	}

	for providerID, providerInfo := range providers {
		providerPath := "providers." + providerID
		providerMap, err := requireMapping(providerInfo, providerPath)
		if err != nil {
			return err
		}
		defaultModel := providerMap["default_model"]
		if !isNonEmptyString(defaultModel) {
			return catalogErrorf("%s.default_model must be a non-empty string", providerPath)
		}
		defaultModelStr := defaultModel.(string)

		modelsValue, ok := providerMap["models"]
		if !ok {
			modelsValue = nil
		}
		models, err := requireList(modelsValue, providerPath+".models")
		if err != nil {
			return err
		}
		if len(models) == 0 {
			return catalogErrorf("%s.models must not be empty", providerPath)
		}

		seen := map[string]struct{}{}
		defaultFound := false
		for idx, model := range models {
			modelPath := fmt.Sprintf("%s.models[%d]", providerPath, idx)
			modelMap, err := requireMapping(model, modelPath)
			if err != nil {
				return err
			}
			modelID := modelMap["id"]
			if !isNonEmptyString(modelID) {
				return catalogErrorf("%s.id must be a non-empty string", modelPath)
			}
			modelIDStr := modelID.(string)
			if _, dup := seen[modelIDStr]; dup {
				return catalogErrorf("Duplicate model id %s in %s.models", pyRepr(modelIDStr), providerPath)
			}
			seen[modelIDStr] = struct{}{}
			if modelIDStr == defaultModelStr {
				defaultFound = true
			}

			if !isNonEmptyString(modelMap["label"]) {
				return catalogErrorf("%s.label must be a non-empty string", modelPath)
			}
			if !isBool(modelMap["recommended"]) {
				return catalogErrorf("%s.recommended must be a boolean", modelPath)
			}
			for _, key := range []string{"max_tokens", "max_context_tokens"} {
				if _, ok := positiveInt(modelMap[key]); !ok {
					return catalogErrorf("%s.%s must be a positive integer", modelPath, key)
				}
			}
			if pricing, ok := modelMap["pricing_usd_per_mtok"]; ok && pricing != nil {
				if err := validatePricing(pricing, modelPath+".pricing_usd_per_mtok"); err != nil {
					return err
				}
			}
			if supportsVision, ok := modelMap["supports_vision"]; ok && supportsVision != nil {
				if !isBool(supportsVision) {
					return catalogErrorf("%s.supports_vision must be a boolean when present", modelPath)
				}
			}
		}
		if !defaultFound {
			return catalogErrorf("%s.default_model=%s is not present in %s.models",
				providerPath, pyRepr(defaultModelStr), providerPath)
		}
	}

	presetsValue, ok := data["presets"]
	if !ok {
		presetsValue = nil
	}
	presets, err := requireMapping(presetsValue, "presets")
	if err != nil {
		return err
	}
	for presetID, presetInfo := range presets {
		presetPath := "presets." + presetID
		presetMap, err := requireMapping(presetInfo, presetPath)
		if err != nil {
			return err
		}
		if !isNonEmptyString(presetMap["provider"]) {
			return catalogErrorf("%s.provider must be a non-empty string", presetPath)
		}
		if value, ok := presetMap["model"]; ok && value != nil && !isNonEmptyString(value) {
			return catalogErrorf("%s.model must be a non-empty string when present", presetPath)
		}
		if value, ok := presetMap["api_base"]; ok && value != nil && !isNonEmptyString(value) {
			return catalogErrorf("%s.api_base must be a non-empty string when present", presetPath)
		}
		if value, ok := presetMap["api_key_env_var"]; ok && value != nil && !isNonEmptyString(value) {
			return catalogErrorf("%s.api_key_env_var must be a non-empty string when present", presetPath)
		}
		for _, key := range []string{"max_tokens", "max_context_tokens"} {
			if _, ok := positiveInt(presetMap[key]); !ok {
				return catalogErrorf("%s.%s must be a positive integer", presetPath, key)
			}
		}
		if modelChoices, ok := presetMap["model_choices"]; ok && modelChoices != nil {
			choices, err := requireList(modelChoices, presetPath+".model_choices")
			if err != nil {
				return err
			}
			for idx, choice := range choices {
				choicePath := fmt.Sprintf("%s.model_choices[%d]", presetPath, idx)
				choiceMap, err := requireMapping(choice, choicePath)
				if err != nil {
					return err
				}
				if !isNonEmptyString(choiceMap["id"]) {
					return catalogErrorf("%s.id must be a non-empty string", choicePath)
				}
				if !isNonEmptyString(choiceMap["label"]) {
					return catalogErrorf("%s.label must be a non-empty string", choicePath)
				}
				if !isBool(choiceMap["recommended"]) {
					return catalogErrorf("%s.recommended must be a boolean", choicePath)
				}
			}
		}
	}
	return nil
}

func pyRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if string(r) == quote {
				b.WriteString(`\`)
			}
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}

func orderedObjectKeys(raw json.RawMessage) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := tok.(json.Delim); !ok || delim != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var keys []string
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyTok.(string)
		if !ok {
			return nil, fmt.Errorf("non-string key")
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}

func buildCatalog(raw []byte, root map[string]any) (*Catalog, error) {
	var rootRaw map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rootRaw); err != nil {
		return nil, catalogErrorf("Model catalogue JSON is invalid: %v", err)
	}
	providerOrder, err := orderedObjectKeys(rootRaw["providers"])
	if err != nil {
		return nil, catalogErrorf("providers must be an object")
	}
	presetOrder, err := orderedObjectKeys(rootRaw["presets"])
	if err != nil {
		return nil, catalogErrorf("presets must be an object")
	}

	cat := &Catalog{
		ProviderOrder: providerOrder,
		Providers:     make(map[string]*ProviderInfo, len(providerOrder)),
		PresetOrder:   presetOrder,
		Presets:       make(map[string]*Preset, len(presetOrder)),
	}
	providersMap := root["providers"].(map[string]any)
	for _, providerID := range providerOrder {
		providerMap := providersMap[providerID].(map[string]any)
		info := &ProviderInfo{DefaultModel: providerMap["default_model"].(string)}
		for _, modelValue := range providerMap["models"].([]any) {
			modelMap := modelValue.(map[string]any)
			entry := ModelEntry{
				ID:          modelMap["id"].(string),
				Label:       modelMap["label"].(string),
				Recommended: modelMap["recommended"].(bool),
			}
			entry.MaxTokens, _ = positiveInt(modelMap["max_tokens"])
			entry.MaxContextTokens, _ = positiveInt(modelMap["max_context_tokens"])
			if flag, ok := modelMap["supports_vision"].(bool); ok {
				entry.SupportsVision = flag
				entry.SupportsVisionSet = true
			}
			if pricingValue, ok := modelMap["pricing_usd_per_mtok"]; ok && pricingValue != nil {
				pricingMap := pricingValue.(map[string]any)
				entry.PricingUSDPerMTok = make(map[string]float64, len(pricingMap))
				for key, rate := range pricingMap {
					if num, ok := rate.(json.Number); ok {
						f, _ := num.Float64()
						entry.PricingUSDPerMTok[key] = f
					}
				}
				entry.HasPricingUSDPerMTok = true
			}
			info.Models = append(info.Models, entry)
		}
		cat.Providers[providerID] = info
	}

	presetsMap := root["presets"].(map[string]any)
	for _, presetID := range presetOrder {
		presetMap := presetsMap[presetID].(map[string]any)
		preset := &Preset{Provider: presetMap["provider"].(string)}
		preset.MaxTokens, _ = positiveInt(presetMap["max_tokens"])
		preset.MaxContextTokens, _ = positiveInt(presetMap["max_context_tokens"])
		if value, ok := presetMap["model"].(string); ok {
			preset.Model = value
			preset.HasModel = true
		}
		if value, ok := presetMap["api_base"].(string); ok {
			preset.APIBase = value
			preset.HasAPIBase = true
		}
		if value, ok := presetMap["api_key_env_var"].(string); ok {
			preset.APIKeyEnvVar = value
			preset.HasAPIKeyEnvVar = true
		}
		if choicesValue, ok := presetMap["model_choices"].([]any); ok {
			preset.HasModelChoices = true
			for _, choiceValue := range choicesValue {
				choiceMap := choiceValue.(map[string]any)
				preset.ModelChoices = append(preset.ModelChoices, ModelChoice{
					ID:          choiceMap["id"].(string),
					Label:       choiceMap["label"].(string),
					Recommended: choiceMap["recommended"].(bool),
				})
			}
		}
		cat.Presets[presetID] = preset
	}
	return cat, nil
}

func parseModelCatalog(raw []byte) (*Catalog, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var data any
	if err := dec.Decode(&data); err != nil {
		return nil, catalogErrorf("Model catalogue JSON is invalid: %v", err)
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, catalogErrorf("Model catalogue JSON is invalid: extra data")
	}
	root, err := requireMapping(data, "root")
	if err != nil {
		return nil, err
	}
	if err := validateModelCatalog(root); err != nil {
		return nil, err
	}
	return buildCatalog(raw, root)
}

var catalogCache struct {
	mu     sync.Mutex
	loaded bool
	value  *Catalog
}

func LoadModelCatalog() (*Catalog, error) {
	catalogCache.mu.Lock()
	defer catalogCache.mu.Unlock()
	if catalogCache.loaded {
		return catalogCache.value, nil
	}
	value, err := parseModelCatalog(embeddedModelCatalog)
	if err != nil {
		return nil, err
	}
	catalogCache.value = value
	catalogCache.loaded = true
	return value, nil
}

func ResetModelCatalogCache() {
	catalogCache.mu.Lock()
	catalogCache.loaded = false
	catalogCache.value = nil
	catalogCache.mu.Unlock()
}

func mustLoadModelCatalog() *Catalog {
	cat, err := LoadModelCatalog()
	if err != nil {
		panic(err)
	}
	return cat
}

var managedCloudProviders = map[string]struct{}{"hive": {}}

func hideManagedProviders() bool {
	return strings.TrimSpace(os.Getenv("HIVE_CLOUD_BASE")) == ""
}

func GetModelsCatalogue() map[string][]ModelEntry {
	cat := mustLoadModelCatalog()
	hide := hideManagedProviders()
	out := make(map[string][]ModelEntry, len(cat.ProviderOrder))
	for _, providerID := range cat.ProviderOrder {
		if hide {
			if _, managed := managedCloudProviders[providerID]; managed {
				continue
			}
		}
		models := cat.Providers[providerID].Models
		copied := make([]ModelEntry, 0, len(models))
		for _, model := range models {
			copied = append(copied, model.Clone())
		}
		out[providerID] = copied
	}
	return out
}

func GetDefaultModels() map[string]string {
	cat := mustLoadModelCatalog()
	out := make(map[string]string, len(cat.ProviderOrder))
	for _, providerID := range cat.ProviderOrder {
		out[providerID] = cat.Providers[providerID].DefaultModel
	}
	return out
}

func GetProviderModels(provider string) []ModelEntry {
	cat := mustLoadModelCatalog()
	info, ok := cat.Providers[provider]
	if !ok || info == nil {
		return nil
	}
	out := make([]ModelEntry, 0, len(info.Models))
	for _, model := range info.Models {
		out = append(out, model.Clone())
	}
	return out
}

func GetDefaultModel(provider string) (string, bool) {
	cat := mustLoadModelCatalog()
	info, ok := cat.Providers[provider]
	if !ok || info == nil {
		return "", false
	}
	return info.DefaultModel, true
}

func FindModel(provider, modelID string) (ModelEntry, bool) {
	cat := mustLoadModelCatalog()
	info, ok := cat.Providers[provider]
	if !ok || info == nil {
		return ModelEntry{}, false
	}
	for _, model := range info.Models {
		if model.ID == modelID {
			return model.Clone(), true
		}
	}
	return ModelEntry{}, false
}

func FindModelAnyProvider(modelID string) (string, ModelEntry, bool) {
	cat := mustLoadModelCatalog()
	for _, providerID := range cat.ProviderOrder {
		for _, model := range cat.Providers[providerID].Models {
			if model.ID == modelID {
				return providerID, model.Clone(), true
			}
		}
	}
	return "", ModelEntry{}, false
}

func GetModelLimits(provider, modelID string) (int, int, bool) {
	model, ok := FindModel(provider, modelID)
	if !ok {
		return 0, 0, false
	}
	return model.MaxTokens, model.MaxContextTokens, true
}

func GetModelPricing(modelID string) (map[string]float64, bool) {
	if modelID == "" {
		return nil, false
	}
	cat := mustLoadModelCatalog()
	for _, providerID := range cat.ProviderOrder {
		for _, model := range cat.Providers[providerID].Models {
			if model.ID == modelID {
				if !model.HasPricingUSDPerMTok || model.PricingUSDPerMTok == nil {
					return nil, false
				}
				out := make(map[string]float64, len(model.PricingUSDPerMTok))
				for key, rate := range model.PricingUSDPerMTok {
					out[key] = rate
				}
				return out, true
			}
		}
	}
	return nil, false
}

func ModelSupportsVision(modelID string) bool {
	if modelID == "" {
		return true
	}
	candidates := []string{modelID}
	if idx := strings.Index(modelID, "/"); idx >= 0 {
		candidates = append(candidates, modelID[idx+1:])
	}
	cat := mustLoadModelCatalog()
	for _, candidate := range candidates {
		for _, providerID := range cat.ProviderOrder {
			for _, model := range cat.Providers[providerID].Models {
				if model.ID == candidate {
					if model.SupportsVisionSet {
						return model.SupportsVision
					}
					return true
				}
			}
		}
	}
	return true
}

func GetPreset(presetID string) (*Preset, bool) {
	cat := mustLoadModelCatalog()
	preset, ok := cat.Presets[presetID]
	if !ok || preset == nil {
		return nil, false
	}
	copied := *preset
	if preset.ModelChoices != nil {
		copied.ModelChoices = append([]ModelChoice(nil), preset.ModelChoices...)
	}
	return &copied, true
}

func GetPresets() map[string]*Preset {
	cat := mustLoadModelCatalog()
	out := make(map[string]*Preset, len(cat.Presets))
	for presetID, preset := range cat.Presets {
		copied := *preset
		if preset.ModelChoices != nil {
			copied.ModelChoices = append([]ModelChoice(nil), preset.ModelChoices...)
		}
		out[presetID] = &copied
	}
	return out
}
