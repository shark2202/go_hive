package skills

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/atomicfs"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

const DefaultRegistryURL = "https://raw.githubusercontent.com/hive-skill-registry/hive-skill-registry/main/skill_index.json"

const CacheTTLSeconds = 3600

type RegistryClient struct {
	url          string
	indexPath    string
	metadataPath string

	httpFetch      func(url string) ([]byte, error)
	fetchIndexHook func(forceRefresh bool) map[string]any
}

func NewRegistryClient(registryURL, cacheDir string) *RegistryClient {
	url := registryURL
	if url == "" {
		url = os.Getenv("HIVE_REGISTRY_URL")
	}
	if url == "" {
		url = DefaultRegistryURL
	}
	if cacheDir == "" {
		if home, err := hivehome.Resolve(); err == nil {
			cacheDir = filepath.Join(home.Root, "registry_cache")
		} else {
			cacheDir = filepath.Join(os.TempDir(), "hive-registry-cache")
		}
	}
	return &RegistryClient{
		url:          url,
		indexPath:    filepath.Join(cacheDir, "skill_index.json"),
		metadataPath: filepath.Join(cacheDir, "metadata.json"),
	}
}

func (c *RegistryClient) FetchIndex(forceRefresh bool) map[string]any {
	if c.fetchIndexHook != nil {
		return c.fetchIndexHook(forceRefresh)
	}
	if !forceRefresh && c.isCacheFresh() {
		if cached := c.LoadCache(); cached != nil {
			return cached
		}
	}

	raw, err := c.fetchHTTP(c.url)
	if err != nil || raw == nil {
		return c.LoadCache()
	}

	var data any
	if err := json.Unmarshal(raw, &data); err != nil {
		return c.LoadCache()
	}
	obj, ok := data.(map[string]any)
	if !ok {
		return c.LoadCache()
	}
	c.SaveCache(obj)
	return obj
}

func (c *RegistryClient) Search(query string) []map[string]any {
	index := c.FetchIndex(false)
	if len(index) == 0 {
		return []map[string]any{}
	}
	skills, ok := index["skills"].([]any)
	if !ok {
		return []map[string]any{}
	}
	q := strings.ToLower(query)
	results := []map[string]any{}
	for _, item := range skills {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := strings.ToLower(pyStr(entry["name"]))
		description := strings.ToLower(pyStr(entry["description"]))
		tags := strings.ToLower(joinAny(entry["tags"], " "))
		if strings.Contains(name, q) || strings.Contains(description, q) || strings.Contains(tags, q) {
			results = append(results, entry)
		}
	}
	return results
}

func (c *RegistryClient) GetSkillEntry(name string) map[string]any {
	index := c.FetchIndex(false)
	if len(index) == 0 {
		return nil
	}
	skills, _ := index["skills"].([]any)
	for _, item := range skills {
		entry, ok := item.(map[string]any)
		if ok && entry["name"] == name {
			return entry
		}
	}
	return nil
}

func (c *RegistryClient) GetPack(packName string) []string {
	index := c.FetchIndex(false)
	if len(index) == 0 {
		return nil
	}
	packs, _ := index["packs"].([]any)
	for _, item := range packs {
		pack, ok := item.(map[string]any)
		if !ok || pack["name"] != packName {
			continue
		}
		list, ok := pack["skills"].([]any)
		if !ok {
			continue
		}
		out := make([]string, 0, len(list))
		for _, s := range list {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

type GitSource struct {
	GitURL       string
	Subdirectory *string
}

func (c *RegistryClient) ResolveGitURL(name string) *GitSource {
	entry := c.GetSkillEntry(name)
	if entry == nil {
		return nil
	}
	gitURL := entry["git_url"]
	if gitURL == nil {
		return nil
	}
	urlStr, ok := gitURL.(string)
	if !ok || urlStr == "" {
		return nil
	}
	src := &GitSource{GitURL: urlStr}
	if sub, ok := entry["subdirectory"].(string); ok && sub != "" {
		src.Subdirectory = &sub
	}
	return src
}

func (c *RegistryClient) LoadCache() map[string]any {
	data, err := os.ReadFile(c.indexPath)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	obj, _ := out.(map[string]any)
	return obj
}

func (c *RegistryClient) SaveCache(data map[string]any) {
	payload, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	if err := atomicfs.WriteFile(c.indexPath, payload, 0o644); err != nil {
		return
	}
	meta := map[string]any{"last_fetched": time.Now().UTC().Format(time.RFC3339Nano)}
	metaPayload, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	_ = atomicfs.WriteFile(c.metadataPath, metaPayload, 0o644)
}

func (c *RegistryClient) IndexPath() string { return c.indexPath }

func (c *RegistryClient) MetadataPath() string { return c.metadataPath }

func (c *RegistryClient) isCacheFresh() bool {
	data, err := os.ReadFile(c.metadataPath)
	if err != nil {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	raw, _ := meta["last_fetched"].(string)
	if raw == "" {
		return false
	}
	last, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		if last, err = time.Parse(time.RFC3339, raw); err != nil {
			return false
		}
	}
	return time.Since(last).Seconds() < CacheTTLSeconds
}

func (c *RegistryClient) fetchHTTP(url string) ([]byte, error) {
	if c.httpFetch != nil {
		return c.httpFetch(url)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func joinAny(v any, sep string) string {
	list, ok := v.([]any)
	if !ok {
		return pyStr(v)
	}
	parts := make([]string, 0, len(list))
	for _, item := range list {
		parts = append(parts, pyStr(item))
	}
	return strings.Join(parts, sep)
}
