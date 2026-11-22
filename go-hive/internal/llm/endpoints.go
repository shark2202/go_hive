package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/credentials"
	"github.com/shark2202/go_hive/go-hive/internal/hivehome"
)

const (
	Magpie        = "magpie"
	MagpieAPIBase = "http://127.0.0.1:3425/v1"
)

const (
	endpointNoKey       = "no-key"
	endpointListTTL     = 60.0
	endpointListTimeout = 10.0
	endpointProbeTime   = 1.0
)

var endpointPlaceholder = map[string]string{Magpie: "magpie"}

var endpointNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

var endpointContextFields = []string{"context_length", "max_model_len", "max_context_tokens", "context_window"}

var endpointLabelFields = []string{"magpie_label", "display_name", "name"}

type Endpoint struct {
	Name    string
	APIBase string
	Builtin bool
}

type EndpointError struct{ Message string }

func (e *EndpointError) Error() string { return e.Message }

func ValidName(name string) bool { return endpointNamePattern.MatchString(name) }

func loadHiveConfig() map[string]any {
	home, err := hivehome.Resolve()
	if err != nil {
		return map[string]any{}
	}
	raw, err := os.ReadFile(home.ConfigurationFile())
	if err != nil {
		return map[string]any{}
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var config map[string]any
	if err := decoder.Decode(&config); err != nil || config == nil {
		return map[string]any{}
	}
	return config
}

func Endpoints() map[string]Endpoint {
	found := map[string]Endpoint{
		Magpie: {Name: Magpie, APIBase: MagpieAPIBase, Builtin: true},
	}
	raw, _ := loadHiveConfig()["model_endpoints"].(map[string]any)
	for name, entry := range raw {
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		base, ok := entryMap["api_base"].(string)
		if !ValidName(name) || !ok || strings.TrimSpace(base) == "" {
			continue
		}
		found[name] = Endpoint{
			Name:    name,
			APIBase: strings.TrimRight(strings.TrimSpace(base), "/"),
			Builtin: name == Magpie,
		}
	}
	return found
}

func Get(name string) (Endpoint, bool) {
	endpoint, ok := Endpoints()[name]
	return endpoint, ok
}

func CredentialID(name string) string { return "endpoint_" + name }

func StoredKey(name string) (string, bool) {
	if os.Getenv("HIVE_CREDENTIAL_KEY") == "" {
		return "", false
	}
	store, err := credentials.WithEncryptedStorage("")
	if err != nil {
		return "", false
	}
	key, err := store.Get(CredentialID(name))
	if err != nil || key == nil || *key == "" {
		return "", false
	}
	return *key, true
}

func KeyFor(name string) string {
	if stored, ok := StoredKey(name); ok {
		return stored
	}
	if placeholder, ok := endpointPlaceholder[name]; ok {
		return placeholder
	}
	return endpointNoKey
}

type endpointCacheEntry struct {
	stamp   float64
	apiBase string
	models  []map[string]any
}

var endpointCache = struct {
	mu      sync.Mutex
	entries map[string]endpointCacheEntry
}{entries: map[string]endpointCacheEntry{}}

var endpointClock = monotonicSeconds

var endpointHTTPGet = func(ctx context.Context, url string, headers map[string]string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	return http.DefaultClient.Do(req)
}

func ListModels(name string, fresh bool) ([]map[string]any, error) {
	endpoint, ok := Get(name)
	if !ok {
		return nil, &EndpointError{Message: fmt.Sprintf("There's no endpoint %s", pyRepr(name))}
	}
	endpointCache.mu.Lock()
	cached, found := endpointCache.entries[name]
	endpointCache.mu.Unlock()
	if !fresh && found && cached.apiBase == endpoint.APIBase && endpointClock()-cached.stamp < endpointListTTL {
		return cached.models, nil
	}
	models, err := fetchEndpointModels(endpoint, endpointListTimeout)
	if err != nil {
		return nil, err
	}
	endpointCache.mu.Lock()
	endpointCache.entries[name] = endpointCacheEntry{
		stamp:   endpointClock(),
		apiBase: endpoint.APIBase,
		models:  models,
	}
	endpointCache.mu.Unlock()
	return models, nil
}

func Probe(name string) bool {
	endpoint, ok := Get(name)
	if !ok {
		return false
	}
	models, err := fetchEndpointModels(endpoint, endpointProbeTime)
	if err != nil {
		return false
	}
	endpointCache.mu.Lock()
	endpointCache.entries[name] = endpointCacheEntry{
		stamp:   endpointClock(),
		apiBase: endpoint.APIBase,
		models:  models,
	}
	endpointCache.mu.Unlock()
	return true
}

func Forget(name string) {
	endpointCache.mu.Lock()
	delete(endpointCache.entries, name)
	endpointCache.mu.Unlock()
}

func fetchEndpointModels(endpoint Endpoint, timeout float64) ([]map[string]any, error) {
	url := endpoint.APIBase + "/models"
	ctx, cancel := context.WithTimeout(context.Background(), secondsDuration(timeout))
	defer cancel()
	response, err := endpointHTTPGet(ctx, url, map[string]string{
		"Authorization": "Bearer " + KeyFor(endpoint.Name),
	})
	if err != nil {
		return nil, &EndpointError{Message: fmt.Sprintf("Can't reach %s: %s", url, httpErrorName(err))}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return nil, &EndpointError{Message: fmt.Sprintf("%s refused the key (HTTP %d)", url, response.StatusCode)}
	}
	if response.StatusCode != http.StatusOK {
		return nil, &EndpointError{Message: fmt.Sprintf("%s answered HTTP %d", url, response.StatusCode)}
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, &EndpointError{Message: fmt.Sprintf("%s didn't answer with JSON", url)}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, &EndpointError{Message: fmt.Sprintf("%s didn't answer with JSON", url)}
	}
	items := payload
	if object, ok := payload.(map[string]any); ok {
		items = object["data"]
	}
	list, ok := items.([]any)
	if !ok {
		return nil, &EndpointError{Message: fmt.Sprintf("%s didn't answer with a list of models", url)}
	}
	models := make([]map[string]any, 0, len(list))
	for _, item := range list {
		details, isObject := item.(map[string]any)
		modelID, isString := item.(string)
		if isObject {
			modelID, isString = details["id"].(string)
		}
		if !isString || modelID == "" {
			continue
		}
		if !isObject {
			details = map[string]any{}
		}
		models = append(models, map[string]any{
			"id":                 modelID,
			"label":              endpointLabelOf(details),
			"max_context_tokens": endpointContextOf(details),
		})
	}
	sort.SliceStable(models, func(i, j int) bool {
		return strings.ToLower(models[i]["id"].(string)) < strings.ToLower(models[j]["id"].(string))
	})
	return models, nil
}

func endpointLabelOf(item map[string]any) any {
	for _, field := range endpointLabelFields {
		value, ok := item[field].(string)
		if ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return nil
}

func endpointContextOf(item map[string]any) any {
	for _, field := range endpointContextFields {
		number, ok := item[field].(json.Number)
		if !ok {
			continue
		}
		text := number.String()
		if strings.ContainsAny(text, ".eE") {
			continue
		}
		value, err := strconv.ParseInt(text, 10, 64)
		if err != nil || value <= 0 {
			continue
		}
		return int(value)
	}
	return nil
}

func httpErrorName(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "TimeoutException"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "TimeoutException"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "ConnectError"
	}
	return "HTTPError"
}

func secondsDuration(seconds float64) time.Duration {
	return time.Duration(seconds * float64(time.Second))
}
