// Batch 7 lane 1 of the integration port: the DevOps hosting group — GitLab
// (9 actions), AWS S3 (8), Vercel (7) and Docker Hub (7).
//
// Upstream reference:
// tools/src/aden_tools/tools/{gitlab,aws_s3,vercel,docker_hub}_tool.
// Action names, parameter names/defaults and response shapes are preserved.
//
// Deliberate limits:
//   - GitLab, Vercel and Docker Hub resolve a single bearer/PAT credential
//     through ModuleConfig (GITLAB_TOKEN, VERCEL_TOKEN, DOCKER_HUB_TOKEN), so
//     WithCredential covers tests. GITLAB_URL optionally relocates a
//     self-hosted instance; WithBaseURL wins over it.
//   - AWS S3 follows the upstream environment-only contract
//     (AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION) and signs every
//     request with SigV4 in-process, like _sign_request/_s3_request.
//   - Every networked action runs through Runtime.HTTP, so each one is
//     exercisable against an httptest server with WithBaseURL. s3_generate_
//     presigned_url is the one exception: upstream it only signs a URL and
//     never calls the API, and that is preserved here.
//   - HTTP failures mirror the upstream per-module helpers, including the
//     exact 401/403/404/429 payloads (and the fact that GitLab's POST path
//     does not special-case 404/429 while GET/PUT do). One ordering nuance:
//     required-argument checks run before credential resolution here, while
//     upstream resolves the token first; the returned error is the same class
//     and no request is made either way.
//   - `assignee_ids` parsing: upstream raises ValueError on a non-numeric
//     entry; here it becomes an {"error": ...} value, because tool failures
//     are values in this port.
package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/tools"
)

const (
	batch7Lane1MaxBody       = 8 << 20
	batch7Lane1MaxError      = 500
	batch7Lane1GitLabDefault = "https://gitlab.com"
	batch7Lane1S3DefaultHost = "https://s3.amazonaws.com"
)

// InstallBatch7Lane1 publishes the batch-7 lane-1 tools (gitlab, aws_s3,
// vercel, docker_hub) into a shared registry and returns the tool count.
func InstallBatch7Lane1(target *tools.ToolRegistry) int {
	return newBatch7Lane1Registry().Install(target)
}

// newBatch7Lane1Registry builds a registry containing only the lane-1 tools,
// sharing the Runtime surface used by the rest of the port.
func newBatch7Lane1Registry(opts ...Option) *Registry {
	runtime := &Runtime{
		HTTP:    newHTTPClient(nil),
		Now:     time.Now,
		Modules: batch7Lane1Modules(),
		Getenv:  os.Getenv,
	}
	for _, opt := range opts {
		opt(runtime)
	}
	registry := NewRegistry(runtime)
	for _, tool := range []*Tool{
		batch7Lane1GitLabTool,
		batch7Lane1S3Tool,
		batch7Lane1VercelTool,
		batch7Lane1DockerHubTool,
	} {
		registry.AddTool(tool)
	}
	for action, impl := range batch7Lane1GitLabImpls() {
		registry.AddImpl("gitlab", action, impl)
	}
	for action, impl := range batch7Lane1S3Impls() {
		registry.AddImpl("aws_s3", action, impl)
	}
	for action, impl := range batch7Lane1VercelImpls() {
		registry.AddImpl("vercel", action, impl)
	}
	for action, impl := range batch7Lane1DockerHubImpls() {
		registry.AddImpl("docker_hub", action, impl)
	}
	return registry
}

// batch7Lane1Modules declares the public endpoints and credential variables.
// AWS S3 keeps an empty BaseURL: its host is derived per bucket from the
// region unless WithBaseURL points it at a fake server.
func batch7Lane1Modules() map[string]*ModuleConfig {
	return map[string]*ModuleConfig{
		"gitlab_tool": {
			ID: "gitlab_tool", BaseURL: batch7Lane1GitLabDefault,
			EnvVars: []string{"GITLAB_TOKEN"},
		},
		"aws_s3_tool": {
			ID:      "aws_s3_tool",
			EnvVars: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"},
		},
		"vercel_tool": {
			ID: "vercel_tool", BaseURL: "https://api.vercel.com",
			EnvVars: []string{"VERCEL_TOKEN"},
		},
		"docker_hub_tool": {
			ID: "docker_hub_tool", BaseURL: "https://hub.docker.com/v2",
			EnvVars: []string{"DOCKER_HUB_TOKEN"},
		},
	}
}

// batch7Lane1GitLabTool publishes the nine GitLab REST v4 actions.
var batch7Lane1GitLabTool = &Tool{
	ID:          "gitlab_tool",
	Name:        "gitlab",
	DisplayName: "GitLab",
	Summary:     "GitLab projects, issues and merge requests (9 actions).",
	Actions: []Action{
		{
			Name:    "gitlab_list_projects",
			Summary: "List GitLab projects.",
			Source:  "gitlab_tool/gitlab_tool.py",
			Params: map[string]any{
				"search":     paramDefault("string", ""),
				"owned":      paramDefault("boolean", false),
				"membership": paramDefault("boolean", true),
				"per_page":   paramDefault("integer", 20),
				"page":       paramDefault("integer", 1),
			},
		},
		{
			Name:     "gitlab_get_project",
			Summary:  "Get details about a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
			},
		},
		{
			Name:     "gitlab_list_issues",
			Summary:  "List issues in a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
				"state":      paramDefault("string", "opened"),
				"labels":     paramDefault("string", ""),
				"search":     paramDefault("string", ""),
				"per_page":   paramDefault("integer", 20),
				"page":       paramDefault("integer", 1),
			},
		},
		{
			Name:     "gitlab_get_issue",
			Summary:  "Get details about a specific GitLab issue.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "issue_iid"},
			Params: map[string]any{
				"project_id": param("string"),
				"issue_iid":  param("integer"),
			},
		},
		{
			Name:     "gitlab_create_issue",
			Summary:  "Create a new issue in a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "title"},
			Params: map[string]any{
				"project_id":   param("string"),
				"title":        param("string"),
				"description":  paramDefault("string", ""),
				"labels":       paramDefault("string", ""),
				"assignee_ids": paramDefault("string", ""),
			},
		},
		{
			Name:     "gitlab_list_merge_requests",
			Summary:  "List merge requests in a GitLab project.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
				"state":      paramDefault("string", "opened"),
				"per_page":   paramDefault("integer", 20),
				"page":       paramDefault("integer", 1),
			},
		},
		{
			Name:     "gitlab_update_issue",
			Summary:  "Update an existing GitLab issue.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "issue_iid"},
			Params: map[string]any{
				"project_id":   param("string"),
				"issue_iid":    param("integer"),
				"title":        paramDefault("string", ""),
				"description":  paramDefault("string", ""),
				"state_event":  paramDefault("string", ""),
				"labels":       paramDefault("string", ""),
				"assignee_ids": paramDefault("string", ""),
			},
		},
		{
			Name:     "gitlab_get_merge_request",
			Summary:  "Get details about a specific GitLab merge request.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "merge_request_iid"},
			Params: map[string]any{
				"project_id":        param("string"),
				"merge_request_iid": param("integer"),
			},
		},
		{
			Name:     "gitlab_create_merge_request_note",
			Summary:  "Add a comment (note) to a GitLab merge request.",
			Source:   "gitlab_tool/gitlab_tool.py",
			Required: []string{"project_id", "merge_request_iid", "body"},
			Params: map[string]any{
				"project_id":        param("string"),
				"merge_request_iid": param("integer"),
				"body":              param("string"),
			},
		},
	},
}

func batch7Lane1GitLabImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"gitlab_list_projects":             batch7Lane1GitLabListProjects,
		"gitlab_get_project":               batch7Lane1GitLabGetProject,
		"gitlab_list_issues":               batch7Lane1GitLabListIssues,
		"gitlab_get_issue":                 batch7Lane1GitLabGetIssue,
		"gitlab_create_issue":              batch7Lane1GitLabCreateIssue,
		"gitlab_list_merge_requests":       batch7Lane1GitLabListMergeRequests,
		"gitlab_update_issue":              batch7Lane1GitLabUpdateIssue,
		"gitlab_get_merge_request":         batch7Lane1GitLabGetMergeRequest,
		"gitlab_create_merge_request_note": batch7Lane1GitLabCreateMergeRequestNote,
	}
}

// batch7Lane1S3Tool publishes the eight S3 object-storage actions.
var batch7Lane1S3Tool = &Tool{
	ID:          "aws_s3_tool",
	Name:        "aws_s3",
	DisplayName: "AWS S3",
	Summary:     "AWS S3 buckets and objects over the signed REST API (8 actions).",
	Actions: []Action{
		{
			Name:    "s3_list_buckets",
			Summary: "List all S3 buckets in the account.",
			Source:  "aws_s3_tool/aws_s3_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "s3_list_objects",
			Summary:  "List objects in an S3 bucket.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket"},
			Params: map[string]any{
				"bucket":    param("string"),
				"prefix":    paramDefault("string", ""),
				"delimiter": paramDefault("string", "/"),
				"max_keys":  paramDefault("integer", 100),
			},
		},
		{
			Name:     "s3_get_object",
			Summary:  "Get an object from S3; returns text content for small objects.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket":    param("string"),
				"key":       param("string"),
				"max_bytes": paramDefault("integer", 10000),
			},
		},
		{
			Name:     "s3_put_object",
			Summary:  "Upload a text object to S3.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key", "content"},
			Params: map[string]any{
				"bucket":       param("string"),
				"key":          param("string"),
				"content":      param("string"),
				"content_type": paramDefault("string", "text/plain"),
			},
		},
		{
			Name:     "s3_delete_object",
			Summary:  "Delete an object from S3.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket": param("string"),
				"key":    param("string"),
			},
		},
		{
			Name:     "s3_copy_object",
			Summary:  "Copy an object within or between S3 buckets.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"source_bucket", "source_key", "dest_bucket", "dest_key"},
			Params: map[string]any{
				"source_bucket": param("string"),
				"source_key":    param("string"),
				"dest_bucket":   param("string"),
				"dest_key":      param("string"),
			},
		},
		{
			Name:     "s3_get_object_metadata",
			Summary:  "Get object metadata without downloading content (HEAD request).",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket": param("string"),
				"key":    param("string"),
			},
		},
		{
			Name:     "s3_generate_presigned_url",
			Summary:  "Generate a pre-signed URL for temporary access to an S3 object.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket":     param("string"),
				"key":        param("string"),
				"expires_in": paramDefault("integer", 3600),
			},
		},
	},
}

func batch7Lane1S3Impls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"s3_list_buckets":           batch7Lane1S3ListBuckets,
		"s3_list_objects":           batch7Lane1S3ListObjects,
		"s3_get_object":             batch7Lane1S3GetObject,
		"s3_put_object":             batch7Lane1S3PutObject,
		"s3_delete_object":          batch7Lane1S3DeleteObject,
		"s3_copy_object":            batch7Lane1S3CopyObject,
		"s3_get_object_metadata":    batch7Lane1S3GetObjectMetadata,
		"s3_generate_presigned_url": batch7Lane1S3GeneratePresignedURL,
	}
}

// batch7Lane1VercelTool publishes the seven Vercel REST actions.
var batch7Lane1VercelTool = &Tool{
	ID:          "vercel_tool",
	Name:        "vercel",
	DisplayName: "Vercel",
	Summary:     "Vercel deployments, projects, domains and env vars (7 actions).",
	Actions: []Action{
		{
			Name:    "vercel_list_deployments",
			Summary: "List Vercel deployments, optionally filtered by project.",
			Source:  "vercel_tool/vercel_tool.py",
			Params: map[string]any{
				"project_id": paramDefault("string", ""),
				"limit":      paramDefault("integer", 20),
				"state":      paramDefault("string", ""),
			},
		},
		{
			Name:     "vercel_get_deployment",
			Summary:  "Get details of a specific Vercel deployment.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"deployment_id"},
			Params: map[string]any{
				"deployment_id": param("string"),
			},
		},
		{
			Name:    "vercel_list_projects",
			Summary: "List all Vercel projects.",
			Source:  "vercel_tool/vercel_tool.py",
			Params: map[string]any{
				"limit": paramDefault("integer", 20),
			},
		},
		{
			Name:     "vercel_get_project",
			Summary:  "Get details of a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
			},
		},
		{
			Name:     "vercel_list_project_domains",
			Summary:  "List domains configured for a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
			},
		},
		{
			Name:     "vercel_list_env_vars",
			Summary:  "List environment variables for a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id"},
			Params: map[string]any{
				"project_id": param("string"),
			},
		},
		{
			Name:     "vercel_create_env_var",
			Summary:  "Create an environment variable for a Vercel project.",
			Source:   "vercel_tool/vercel_tool.py",
			Required: []string{"project_id", "key", "value"},
			Params: map[string]any{
				"project_id": param("string"),
				"key":        param("string"),
				"value":      param("string"),
				"target":     paramDefault("string", "production,preview,development"),
				"env_type":   paramDefault("string", "encrypted"),
			},
		},
	},
}

func batch7Lane1VercelImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"vercel_list_deployments":     batch7Lane1VercelListDeployments,
		"vercel_get_deployment":       batch7Lane1VercelGetDeployment,
		"vercel_list_projects":        batch7Lane1VercelListProjects,
		"vercel_get_project":          batch7Lane1VercelGetProject,
		"vercel_list_project_domains": batch7Lane1VercelListProjectDomains,
		"vercel_list_env_vars":        batch7Lane1VercelListEnvVars,
		"vercel_create_env_var":       batch7Lane1VercelCreateEnvVar,
	}
}

// batch7Lane1DockerHubTool publishes the seven Docker Hub registry actions.
var batch7Lane1DockerHubTool = &Tool{
	ID:          "docker_hub_tool",
	Name:        "docker_hub",
	DisplayName: "Docker Hub",
	Summary:     "Docker Hub repository, tag and webhook inspection (7 actions).",
	Actions: []Action{
		{
			Name:     "docker_hub_search",
			Summary:  "Search Docker Hub for repositories.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"max_results": paramDefault("integer", 25),
			},
		},
		{
			Name:    "docker_hub_list_repos",
			Summary: "List repositories for a Docker Hub user or organization.",
			Source:  "docker_hub_tool/docker_hub_tool.py",
			Params: map[string]any{
				"namespace":   paramDefault("string", ""),
				"max_results": paramDefault("integer", 25),
			},
		},
		{
			Name:     "docker_hub_list_tags",
			Summary:  "List tags for a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository"},
			Params: map[string]any{
				"repository":  param("string"),
				"max_results": paramDefault("integer", 25),
			},
		},
		{
			Name:     "docker_hub_get_repo",
			Summary:  "Get detailed information about a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository"},
			Params: map[string]any{
				"repository": param("string"),
			},
		},
		{
			Name:     "docker_hub_get_tag_detail",
			Summary:  "Get detailed information about a specific image tag.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository", "tag"},
			Params: map[string]any{
				"repository": param("string"),
				"tag":        param("string"),
			},
		},
		{
			Name:     "docker_hub_delete_tag",
			Summary:  "Delete a specific tag from a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository", "tag"},
			Params: map[string]any{
				"repository": param("string"),
				"tag":        param("string"),
			},
		},
		{
			Name:     "docker_hub_list_webhooks",
			Summary:  "List webhooks configured for a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository"},
			Params: map[string]any{
				"repository": param("string"),
			},
		},
	},
}

func batch7Lane1DockerHubImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"docker_hub_search":         batch7Lane1DockerHubSearch,
		"docker_hub_list_repos":     batch7Lane1DockerHubListRepos,
		"docker_hub_list_tags":      batch7Lane1DockerHubListTags,
		"docker_hub_get_repo":       batch7Lane1DockerHubGetRepo,
		"docker_hub_get_tag_detail": batch7Lane1DockerHubGetTagDetail,
		"docker_hub_delete_tag":     batch7Lane1DockerHubDeleteTag,
		"docker_hub_list_webhooks":  batch7Lane1DockerHubListWebhooks,
	}
}

// ------------------------------------------------------------ shared bits ---

// batch7Lane1Getenv resolves the runtime environment lookup.
func batch7Lane1Getenv(rt *Runtime) func(string) string {
	if rt != nil && rt.Getenv != nil {
		return rt.Getenv
	}
	return os.Getenv
}

// batch7Lane1ModuleURL resolves a module base URL, honouring WithBaseURL.
func batch7Lane1ModuleURL(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
}

// batch7Lane1ModuleToken resolves the module credential from WithCredential or
// the configured environment lookup.
func batch7Lane1ModuleToken(rt *Runtime, moduleID string) string {
	if rt == nil || rt.Modules == nil {
		return ""
	}
	cfg := rt.Modules[moduleID]
	if cfg == nil {
		return ""
	}
	token, _ := cfg.ResolvedToken(batch7Lane1Getenv(rt))
	return strings.TrimSpace(token)
}

// batch7Lane1HTTP returns the runtime transport, falling back to a client with
// the standard timeout for hand-built runtimes.
func batch7Lane1HTTP(rt *Runtime) *httpClient {
	if rt != nil && rt.HTTP != nil {
		return rt.HTTP
	}
	return newHTTPClient(nil)
}

// batch7Lane1Now resolves the runtime clock.
func batch7Lane1Now(rt *Runtime) time.Time {
	if rt != nil && rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

// batch7Lane1Reply is the status/body/header triple the module helpers inspect.
type batch7Lane1Reply struct {
	Status int
	Body   []byte
	Header http.Header
}

// batch7Lane1Do performs one request with a raw body and reads a bounded body.
func batch7Lane1Do(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	body []byte,
) (batch7Lane1Reply, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return batch7Lane1Reply{}, err
	}
	for name, value := range headers {
		// net/http derives Host from the URL; setting the header is a no-op.
		if strings.EqualFold(name, "host") {
			continue
		}
		req.Header.Set(name, value)
	}
	resp, err := batch7Lane1HTTP(rt).Do(req)
	if err != nil {
		return batch7Lane1Reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, batch7Lane1MaxBody))
	if err != nil {
		return batch7Lane1Reply{}, err
	}
	return batch7Lane1Reply{Status: resp.StatusCode, Body: raw, Header: resp.Header.Clone()}, nil
}

// batch7Lane1JSON performs one JSON request.
func batch7Lane1JSON(
	ctx context.Context,
	rt *Runtime,
	method, target string,
	headers map[string]string,
	payload any,
) (batch7Lane1Reply, error) {
	var body []byte
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return batch7Lane1Reply{}, err
		}
		body = encoded
	}
	return batch7Lane1Do(ctx, rt, method, target, headers, body)
}

// batch7Lane1IsTimeout reports whether err is a deadline or a net timeout.
func batch7Lane1IsTimeout(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// batch7Lane1Truncate caps provider error/description text the same way
// upstream slices response.text[:500].
func batch7Lane1Truncate(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	runes := []rune(text)
	return string(runes[:limit])
}

// batch7Lane1Clamp bounds an integer to [low, high].
func batch7Lane1Clamp(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// batch7Lane1AsMap safely views a decoded JSON object.
func batch7Lane1AsMap(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// batch7Lane1AsSlice safely views a decoded JSON array.
func batch7Lane1AsSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

// batch7Lane1AsString reads a decoded JSON string.
func batch7Lane1AsString(value any) string {
	text, _ := value.(string)
	return text
}

// batch7Lane1AsBool reads a decoded JSON boolean with the upstream
// `.get(key, fallback)` default applied when the field is absent.
func batch7Lane1AsBool(value any, fallback bool) bool {
	if flag, ok := value.(bool); ok {
		return flag
	}
	return fallback
}

// batch7Lane1Number turns integral float64 values (the form encoding/json
// produces) back into integers so responses render like Python ints.
func batch7Lane1Number(value any) any {
	switch typed := value.(type) {
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed)
		}
		return typed
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed
		}
		return typed
	default:
		return value
	}
}

// batch7Lane1IntValue reads a decoded JSON number with a default.
func batch7Lane1IntValue(value any, fallback int) int {
	switch typed := batch7Lane1Number(value).(type) {
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed
		}
	}
	return fallback
}

// batch7Lane1StringList renders a decoded JSON array of strings. A missing
// field produces an empty (non-nil) slice so it encodes as [].
func batch7Lane1StringList(value any) []string {
	items := batch7Lane1AsSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, batch7Lane1AsString(item))
	}
	return out
}

// batch7Lane1Usernames renders a decoded JSON array of user objects as the
// upstream `[a.get("username", "") for a in ...]` list.
func batch7Lane1Usernames(value any) []string {
	items := batch7Lane1AsSlice(value)
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, batch7Lane1AsString(batch7Lane1AsMap(item)["username"]))
	}
	return out
}

// batch7Lane1DecodeAny decodes a JSON body of any shape.
func batch7Lane1DecodeAny(raw []byte) (any, bool) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// batch7Lane1DecodeObject decodes a JSON object body.
func batch7Lane1DecodeObject(raw []byte) (map[string]any, bool) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	if value == nil {
		value = map[string]any{}
	}
	return value, true
}

// batch7Lane1SplitIDs parses the upstream comma-separated integer list used by
// gitlab_create_issue / gitlab_update_issue.
func batch7Lane1SplitIDs(raw string) ([]int, bool) {
	out := make([]int, 0)
	for _, part := range strings.Split(raw, ",") {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		parsed, err := strconv.Atoi(trimmed)
		if err != nil {
			return nil, false
		}
		out = append(out, parsed)
	}
	return out, true
}

// batch7Lane1HeaderOrNil renders a response header the way Python's
// resp.headers.get(name) does: the value or None.
func batch7Lane1HeaderOrNil(header http.Header, name string) any {
	if value := header.Get(name); value != "" {
		return value
	}
	return nil
}

// ------------------------------------------------------------------ GitLab ---

// batch7Lane1GitLabBase resolves the instance root; WithBaseURL wins over
// GITLAB_URL, which wins over gitlab.com.
func batch7Lane1GitLabBase(rt *Runtime) string {
	if base := batch7Lane1ModuleURL(rt, "gitlab_tool"); base != "" && base != batch7Lane1GitLabDefault {
		return base
	}
	if env := strings.TrimSpace(batch7Lane1Getenv(rt)("GITLAB_URL")); env != "" {
		return strings.TrimRight(env, "/")
	}
	return batch7Lane1GitLabDefault
}

// batch7Lane1GitLabCall performs one /api/v4 call and maps HTTP failures to the
// upstream error dicts. Upstream's _post omits the 404/429 branches, so the
// method decides whether those two mappings apply. The boolean reports success.
func batch7Lane1GitLabCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := batch7Lane1ModuleToken(rt, "gitlab_tool")
	if token == "" {
		return map[string]any{
			"error": "GITLAB_TOKEN not set",
			"help":  "Create a personal access token at https://gitlab.com/-/user_settings/personal_access_tokens",
		}, false
	}
	target := batch7Lane1GitLabBase(rt) + "/api/v4" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{"PRIVATE-TOKEN": token}
	if payload != nil {
		headers["Content-Type"] = "application/json"
	}
	reply, err := batch7Lane1JSON(ctx, rt, method, target, headers, payload)
	if err != nil {
		if batch7Lane1IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to GitLab timed out"}, false
		}
		return map[string]any{"error": "GitLab request failed: " + err.Error()}, false
	}
	richErrors := method != http.MethodPost
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your GitLab token."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Insufficient permissions."}, false
	case http.StatusNotFound:
		if richErrors {
			return map[string]any{"error": "Not found."}, false
		}
	case http.StatusTooManyRequests:
		if richErrors {
			return map[string]any{"error": "Rate limited. Try again shortly."}, false
		}
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"GitLab API error %d: %s",
				reply.Status,
				batch7Lane1Truncate(string(reply.Body), batch7Lane1MaxError),
			),
		}, false
	}
	value, ok := batch7Lane1DecodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "GitLab request failed: invalid JSON response"}, false
	}
	return value, true
}

// batch7Lane1GitLabPage renders the clamped per_page/page pair.
func batch7Lane1GitLabPage(input map[string]any) (string, string) {
	perPage := batch7Lane1Clamp(intArg(input, "per_page", 20), 1, 100)
	page := intArg(input, "page", 1)
	if page < 1 {
		page = 1
	}
	return strconv.Itoa(perPage), strconv.Itoa(page)
}

func batch7Lane1GitLabListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	perPage, page := batch7Lane1GitLabPage(input)
	query := url.Values{
		"per_page":   {perPage},
		"page":       {page},
		"membership": {strconv.FormatBool(boolArg(input, "membership", true))},
	}
	if search := stringArg(input, "search"); search != "" {
		query.Set("search", search)
	}
	if boolArg(input, "owned", false) {
		query.Set("owned", "true")
	}
	value, ok := batch7Lane1GitLabCall(ctx, rt, http.MethodGet, "/projects", query, nil)
	if !ok {
		return value, nil
	}
	projects := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(value) {
		project := batch7Lane1AsMap(raw)
		projects = append(projects, map[string]any{
			"id":                  batch7Lane1Number(project["id"]),
			"name":                batch7Lane1AsString(project["name"]),
			"path_with_namespace": batch7Lane1AsString(project["path_with_namespace"]),
			"description":         batch7Lane1Truncate(batch7Lane1AsString(project["description"]), 200),
			"visibility":          batch7Lane1AsString(project["visibility"]),
			"default_branch":      batch7Lane1AsString(project["default_branch"]),
			"web_url":             batch7Lane1AsString(project["web_url"]),
			"star_count":          batch7Lane1IntValue(project["star_count"], 0),
			"last_activity_at":    batch7Lane1AsString(project["last_activity_at"]),
		})
	}
	return map[string]any{"projects": projects, "count": len(projects)}, nil
}

func batch7Lane1GitLabGetProject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	query := url.Values{"statistics": {"true"}}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodGet, "/projects/"+projectID, query, nil,
	)
	if !ok {
		return value, nil
	}
	project, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	stats := batch7Lane1AsMap(project["statistics"])
	return map[string]any{
		"id":                  batch7Lane1Number(project["id"]),
		"name":                batch7Lane1AsString(project["name"]),
		"path_with_namespace": batch7Lane1AsString(project["path_with_namespace"]),
		"description":         batch7Lane1Truncate(batch7Lane1AsString(project["description"]), 500),
		"visibility":          batch7Lane1AsString(project["visibility"]),
		"default_branch":      batch7Lane1AsString(project["default_branch"]),
		"web_url":             batch7Lane1AsString(project["web_url"]),
		"star_count":          batch7Lane1IntValue(project["star_count"], 0),
		"forks_count":         batch7Lane1IntValue(project["forks_count"], 0),
		"open_issues_count":   batch7Lane1IntValue(project["open_issues_count"], 0),
		"commit_count":        batch7Lane1IntValue(stats["commit_count"], 0),
		"created_at":          batch7Lane1AsString(project["created_at"]),
		"last_activity_at":    batch7Lane1AsString(project["last_activity_at"]),
	}, nil
}

func batch7Lane1GitLabListIssues(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	perPage, page := batch7Lane1GitLabPage(input)
	state := stringArg(input, "state")
	if state == "" {
		state = "opened"
	}
	query := url.Values{"state": {state}, "per_page": {perPage}, "page": {page}}
	if labels := stringArg(input, "labels"); labels != "" {
		query.Set("labels", labels)
	}
	if search := stringArg(input, "search"); search != "" {
		query.Set("search", search)
	}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodGet, "/projects/"+projectID+"/issues", query, nil,
	)
	if !ok {
		return value, nil
	}
	issues := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(value) {
		issue := batch7Lane1AsMap(raw)
		issues = append(issues, map[string]any{
			"iid":        batch7Lane1Number(issue["iid"]),
			"title":      batch7Lane1AsString(issue["title"]),
			"state":      batch7Lane1AsString(issue["state"]),
			"labels":     batch7Lane1StringList(issue["labels"]),
			"assignees":  batch7Lane1Usernames(issue["assignees"]),
			"author":     batch7Lane1AsString(batch7Lane1AsMap(issue["author"])["username"]),
			"created_at": batch7Lane1AsString(issue["created_at"]),
			"updated_at": batch7Lane1AsString(issue["updated_at"]),
			"web_url":    batch7Lane1AsString(issue["web_url"]),
		})
	}
	return map[string]any{"issues": issues, "count": len(issues)}, nil
}

func batch7Lane1GitLabGetIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	issueIID := intArg(input, "issue_iid", 0)
	if projectID == "" || issueIID == 0 {
		return map[string]any{"error": "project_id and issue_iid are required"}, nil
	}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodGet,
		fmt.Sprintf("/projects/%s/issues/%d", projectID, issueIID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	issue, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":         batch7Lane1Number(issue["iid"]),
		"title":       batch7Lane1AsString(issue["title"]),
		"description": batch7Lane1Truncate(batch7Lane1AsString(issue["description"]), 1000),
		"state":       batch7Lane1AsString(issue["state"]),
		"labels":      batch7Lane1StringList(issue["labels"]),
		"assignees":   batch7Lane1Usernames(issue["assignees"]),
		"author":      batch7Lane1AsString(batch7Lane1AsMap(issue["author"])["username"]),
		"milestone":   batch7Lane1AsString(batch7Lane1AsMap(issue["milestone"])["title"]),
		"due_date":    issue["due_date"],
		"web_url":     batch7Lane1AsString(issue["web_url"]),
		"created_at":  batch7Lane1AsString(issue["created_at"]),
		"updated_at":  batch7Lane1AsString(issue["updated_at"]),
		"closed_at":   issue["closed_at"],
	}, nil
}

func batch7Lane1GitLabCreateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	title := stringArg(input, "title")
	if projectID == "" || title == "" {
		return map[string]any{"error": "project_id and title are required"}, nil
	}
	body := map[string]any{"title": title}
	if description := stringArg(input, "description"); description != "" {
		body["description"] = description
	}
	if labels := stringArg(input, "labels"); labels != "" {
		body["labels"] = labels
	}
	if raw := stringArg(input, "assignee_ids"); raw != "" {
		ids, ok := batch7Lane1SplitIDs(raw)
		if !ok {
			return map[string]any{"error": "assignee_ids must be a comma-separated list of user IDs"}, nil
		}
		body["assignee_ids"] = ids
	}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodPost, "/projects/"+projectID+"/issues", nil, body,
	)
	if !ok {
		return value, nil
	}
	issue, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":     batch7Lane1Number(issue["iid"]),
		"title":   batch7Lane1AsString(issue["title"]),
		"web_url": batch7Lane1AsString(issue["web_url"]),
		"status":  "created",
	}, nil
}

func batch7Lane1GitLabListMergeRequests(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	perPage, page := batch7Lane1GitLabPage(input)
	state := stringArg(input, "state")
	if state == "" {
		state = "opened"
	}
	query := url.Values{"state": {state}, "per_page": {perPage}, "page": {page}}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodGet, "/projects/"+projectID+"/merge_requests", query, nil,
	)
	if !ok {
		return value, nil
	}
	requests := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(value) {
		request := batch7Lane1AsMap(raw)
		requests = append(requests, map[string]any{
			"iid":           batch7Lane1Number(request["iid"]),
			"title":         batch7Lane1AsString(request["title"]),
			"state":         batch7Lane1AsString(request["state"]),
			"source_branch": batch7Lane1AsString(request["source_branch"]),
			"target_branch": batch7Lane1AsString(request["target_branch"]),
			"author":        batch7Lane1AsString(batch7Lane1AsMap(request["author"])["username"]),
			"web_url":       batch7Lane1AsString(request["web_url"]),
			"created_at":    batch7Lane1AsString(request["created_at"]),
			"updated_at":    batch7Lane1AsString(request["updated_at"]),
		})
	}
	return map[string]any{"merge_requests": requests, "count": len(requests)}, nil
}

func batch7Lane1GitLabUpdateIssue(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	issueIID := intArg(input, "issue_iid", 0)
	if projectID == "" || issueIID == 0 {
		return map[string]any{"error": "project_id and issue_iid are required"}, nil
	}
	body := map[string]any{}
	if title := stringArg(input, "title"); title != "" {
		body["title"] = title
	}
	if description := stringArg(input, "description"); description != "" {
		body["description"] = description
	}
	if event := stringArg(input, "state_event"); event != "" {
		body["state_event"] = event
	}
	if labels := stringArg(input, "labels"); labels != "" {
		body["labels"] = labels
	}
	if raw := stringArg(input, "assignee_ids"); raw != "" {
		ids, ok := batch7Lane1SplitIDs(raw)
		if !ok {
			return map[string]any{"error": "assignee_ids must be a comma-separated list of user IDs"}, nil
		}
		body["assignee_ids"] = ids
	}
	if len(body) == 0 {
		return map[string]any{"error": "At least one field to update is required"}, nil
	}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodPut,
		fmt.Sprintf("/projects/%s/issues/%d", projectID, issueIID), nil, body,
	)
	if !ok {
		return value, nil
	}
	issue, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":     batch7Lane1Number(issue["iid"]),
		"title":   batch7Lane1AsString(issue["title"]),
		"state":   batch7Lane1AsString(issue["state"]),
		"web_url": batch7Lane1AsString(issue["web_url"]),
		"status":  "updated",
	}, nil
}

func batch7Lane1GitLabGetMergeRequest(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	requestIID := intArg(input, "merge_request_iid", 0)
	if projectID == "" || requestIID == 0 {
		return map[string]any{"error": "project_id and merge_request_iid are required"}, nil
	}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodGet,
		fmt.Sprintf("/projects/%s/merge_requests/%d", projectID, requestIID), nil, nil,
	)
	if !ok {
		return value, nil
	}
	request, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"iid":           batch7Lane1Number(request["iid"]),
		"title":         batch7Lane1AsString(request["title"]),
		"description":   batch7Lane1Truncate(batch7Lane1AsString(request["description"]), 1000),
		"state":         batch7Lane1AsString(request["state"]),
		"source_branch": batch7Lane1AsString(request["source_branch"]),
		"target_branch": batch7Lane1AsString(request["target_branch"]),
		"author":        batch7Lane1AsString(batch7Lane1AsMap(request["author"])["username"]),
		"reviewers":     batch7Lane1Usernames(request["reviewers"]),
		"merge_status":  batch7Lane1AsString(request["merge_status"]),
		"has_conflicts": batch7Lane1AsBool(request["has_conflicts"], false),
		"changes_count": request["changes_count"],
		"web_url":       batch7Lane1AsString(request["web_url"]),
		"created_at":    batch7Lane1AsString(request["created_at"]),
		"updated_at":    batch7Lane1AsString(request["updated_at"]),
		"merged_at":     request["merged_at"],
	}, nil
}

func batch7Lane1GitLabCreateMergeRequestNote(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	requestIID := intArg(input, "merge_request_iid", 0)
	body := stringArg(input, "body")
	if projectID == "" || requestIID == 0 || body == "" {
		return map[string]any{"error": "project_id, merge_request_iid, and body are required"}, nil
	}
	value, ok := batch7Lane1GitLabCall(
		ctx, rt, http.MethodPost,
		fmt.Sprintf("/projects/%s/merge_requests/%d/notes", projectID, requestIID), nil,
		map[string]any{"body": body},
	)
	if !ok {
		return value, nil
	}
	note, isObject := value.(map[string]any)
	if !isObject {
		return map[string]any{"error": "Unexpected response format"}, nil
	}
	return map[string]any{
		"id":         batch7Lane1Number(note["id"]),
		"body":       batch7Lane1Truncate(batch7Lane1AsString(note["body"]), 500),
		"author":     batch7Lane1AsString(batch7Lane1AsMap(note["author"])["username"]),
		"created_at": batch7Lane1AsString(note["created_at"]),
		"status":     "created",
	}, nil
}

// ---------------------------------------------------------------- AWS S3 ---

// batch7Lane1S3Credentials is the SigV4 credential bundle.
type batch7Lane1S3Credentials struct {
	AccessKey string
	SecretKey string
	Region    string
}

// batch7Lane1S3Config reads the environment-only AWS credential bundle.
func batch7Lane1S3Config(rt *Runtime) (batch7Lane1S3Credentials, map[string]any) {
	getenv := batch7Lane1Getenv(rt)
	creds := batch7Lane1S3Credentials{
		AccessKey: strings.TrimSpace(getenv("AWS_ACCESS_KEY_ID")),
		SecretKey: strings.TrimSpace(getenv("AWS_SECRET_ACCESS_KEY")),
		Region:    strings.TrimSpace(getenv("AWS_REGION")),
	}
	if creds.Region == "" {
		creds.Region = "us-east-1"
	}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return batch7Lane1S3Credentials{}, map[string]any{
			"error": "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required",
			"help":  "Set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables",
		}
	}
	return creds, nil
}

// batch7Lane1S3HostBase renders the endpoint root for one bucket.
func batch7Lane1S3HostBase(rt *Runtime, bucket, region string) string {
	if base := batch7Lane1ModuleURL(rt, "aws_s3_tool"); base != "" {
		return base
	}
	if bucket == "" {
		return batch7Lane1S3DefaultHost
	}
	return "https://" + bucket + ".s3." + region + ".amazonaws.com"
}

// batch7Lane1S3Escape percent-encodes per AWS canonical rules (quote(safe=”)).
func batch7Lane1S3Escape(value string) string {
	var b strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' || character == '~' {
			b.WriteByte(character)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", character))
	}
	return b.String()
}

// batch7Lane1S3CanonicalQuery renders the sorted canonical query string.
func batch7Lane1S3CanonicalQuery(query map[string]string) string {
	keys := make([]string, 0, len(query))
	for name := range query {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, batch7Lane1S3Escape(name)+"="+batch7Lane1S3Escape(query[name]))
	}
	return strings.Join(parts, "&")
}

// batch7Lane1S3SignKey derives the SigV4 signing key.
func batch7Lane1S3SignKey(secretKey, datestamp, region string) []byte {
	sign := func(key []byte, message string) []byte {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(message))
		return mac.Sum(nil)
	}
	dateKey := sign([]byte("AWS4"+secretKey), datestamp)
	regionKey := sign(dateKey, region)
	serviceKey := sign(regionKey, "s3")
	return sign(serviceKey, "aws4_request")
}

// batch7Lane1S3SignRequest signs one request in place, mirroring
// _sign_request: sorted canonical query, sorted canonical headers, signed
// header list and the AWS4-HMAC-SHA256 Authorization value.
func batch7Lane1S3SignRequest(
	now time.Time,
	method, host, path string,
	query, headers map[string]string,
	body []byte,
	creds batch7Lane1S3Credentials,
) map[string]string {
	stamp := now.UTC()
	datestamp := stamp.Format("20060102")
	amzDate := stamp.Format("20060102T150405Z")
	payloadHash := sha256.Sum256(body)
	payloadHex := hex.EncodeToString(payloadHash[:])

	if headers == nil {
		headers = map[string]string{}
	}
	headers["host"] = host
	headers["x-amz-date"] = amzDate
	headers["x-amz-content-sha256"] = payloadHex

	canonicalQuery := batch7Lane1S3CanonicalQuery(query)

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(strings.ToLower(name))
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headers[name]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		method,
		path,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHex,
	}, "\n")

	scope := datestamp + "/" + creds.Region + "/s3/aws4_request"
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(requestHash[:]),
	}, "\n")

	mac := hmac.New(sha256.New, batch7Lane1S3SignKey(creds.SecretKey, datestamp, creds.Region))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	headers["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + creds.AccessKey + "/" + scope +
		",SignedHeaders=" + signedHeaders + ",Signature=" + signature
	return headers
}

// batch7Lane1S3Call signs and performs one S3 request against the resolved
// endpoint.
func batch7Lane1S3Call(
	ctx context.Context,
	rt *Runtime,
	creds batch7Lane1S3Credentials,
	method, bucket, key string,
	query map[string]string,
	body []byte,
	extra map[string]string,
) (batch7Lane1Reply, error) {
	path := "/"
	if key != "" {
		path = "/" + key
	}
	base := batch7Lane1S3HostBase(rt, bucket, creds.Region)
	target := base + path
	if canonical := batch7Lane1S3CanonicalQuery(query); canonical != "" {
		target += "?" + canonical
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return batch7Lane1Reply{}, err
	}
	headers := make(map[string]string, len(extra)+4)
	for name, value := range extra {
		headers[name] = value
	}
	headers = batch7Lane1S3SignRequest(batch7Lane1Now(rt), method, parsed.Host, path, query, headers, body, creds)
	return batch7Lane1Do(ctx, rt, method, target, headers, body)
}

// batch7Lane1S3Request resolves credentials and performs one signed call,
// returning the upstream-shaped error dict on credential or transport failure.
func batch7Lane1S3Request(
	ctx context.Context,
	rt *Runtime,
	method, bucket, key string,
	query map[string]string,
	body []byte,
	extra map[string]string,
) (batch7Lane1Reply, map[string]any) {
	creds, authErr := batch7Lane1S3Config(rt)
	if authErr != nil {
		return batch7Lane1Reply{}, authErr
	}
	reply, err := batch7Lane1S3Call(ctx, rt, creds, method, bucket, key, query, body, extra)
	if err != nil {
		if batch7Lane1IsTimeout(ctx, err) {
			return batch7Lane1Reply{}, map[string]any{"error": "Request to S3 timed out"}
		}
		return batch7Lane1Reply{}, map[string]any{"error": "S3 request failed: " + err.Error()}
	}
	return reply, nil
}

// batch7Lane1S3HTTPError renders the upstream `HTTP {status}: {text[:500]}`
// error payload.
func batch7Lane1S3HTTPError(reply batch7Lane1Reply) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", reply.Status, batch7Lane1Truncate(string(reply.Body), batch7Lane1MaxError)),
	}
}

// batch7Lane1S3Buckets is the ListAllMyBucketsResult envelope.
type batch7Lane1S3Buckets struct {
	Buckets []struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	} `xml:"Buckets>Bucket"`
}

// batch7Lane1S3ListResult is the ListBucketResult envelope.
type batch7Lane1S3ListResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         string `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
	IsTruncated           string `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func batch7Lane1S3ListBuckets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodGet, "", "", nil, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return batch7Lane1S3HTTPError(reply), nil
	}
	var parsed batch7Lane1S3Buckets
	if err := xml.Unmarshal(reply.Body, &parsed); err != nil {
		return map[string]any{"error": "S3 request failed: invalid XML response"}, nil
	}
	buckets := make([]map[string]any, 0, len(parsed.Buckets))
	for _, bucket := range parsed.Buckets {
		buckets = append(buckets, map[string]any{
			"name":          bucket.Name,
			"creation_date": bucket.CreationDate,
		})
	}
	return map[string]any{"count": len(buckets), "buckets": buckets}, nil
}

func batch7Lane1S3ListObjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := stringArg(input, "bucket")
	if bucket == "" {
		return map[string]any{"error": "bucket is required"}, nil
	}
	query := map[string]string{
		"list-type": "2",
		"max-keys":  strconv.Itoa(intArg(input, "max_keys", 100)),
	}
	if prefix := stringArg(input, "prefix"); prefix != "" {
		query["prefix"] = prefix
	}
	if delimiter := stringArg(input, "delimiter"); delimiter != "" {
		query["delimiter"] = delimiter
	}
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodGet, bucket, "", query, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return batch7Lane1S3HTTPError(reply), nil
	}
	var parsed batch7Lane1S3ListResult
	if err := xml.Unmarshal(reply.Body, &parsed); err != nil {
		return map[string]any{"error": "S3 request failed: invalid XML response"}, nil
	}
	objects := make([]map[string]any, 0, len(parsed.Contents))
	for _, object := range parsed.Contents {
		size, _ := strconv.Atoi(strings.TrimSpace(object.Size))
		objects = append(objects, map[string]any{
			"key":           object.Key,
			"size":          size,
			"last_modified": object.LastModified,
		})
	}
	result := map[string]any{"count": len(objects), "objects": objects}
	if len(parsed.CommonPrefixes) > 0 {
		prefixes := make([]string, 0, len(parsed.CommonPrefixes))
		for _, item := range parsed.CommonPrefixes {
			if item.Prefix != "" {
				prefixes = append(prefixes, item.Prefix)
			}
		}
		if len(prefixes) > 0 {
			result["common_prefixes"] = prefixes
		}
	}
	if parsed.IsTruncated == "true" && parsed.NextContinuationToken != "" {
		result["next_continuation_token"] = parsed.NextContinuationToken
	}
	return result, nil
}

func batch7Lane1S3GetObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := stringArg(input, "bucket")
	key := stringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	maxBytes := intArg(input, "max_bytes", 10000)
	extra := map[string]string{}
	if maxBytes > 0 {
		extra["Range"] = fmt.Sprintf("bytes=0-%d", maxBytes-1)
	}
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodGet, bucket, key, nil, nil, extra)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return batch7Lane1S3HTTPError(reply), nil
	}
	contentType := reply.Header.Get("content-type")
	result := map[string]any{
		"key":           key,
		"content_type":  contentType,
		"size":          batch7Lane1HeaderOrNil(reply.Header, "content-length"),
		"last_modified": batch7Lane1HeaderOrNil(reply.Header, "last-modified"),
		"etag":          batch7Lane1HeaderOrNil(reply.Header, "etag"),
	}
	if strings.Contains(contentType, "text") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") {
		result["content"] = string(reply.Body)
	} else {
		result["content_preview"] = fmt.Sprintf("[binary data, %d bytes]", len(reply.Body))
	}
	return result, nil
}

func batch7Lane1S3PutObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := stringArg(input, "bucket")
	key := stringArg(input, "key")
	content := stringArg(input, "content")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	if content == "" {
		return map[string]any{"error": "content is required"}, nil
	}
	contentType := stringArg(input, "content_type")
	if contentType == "" {
		contentType = "text/plain"
	}
	body := []byte(content)
	extra := map[string]string{"content-type": contentType}
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodPut, bucket, key, nil, body, extra)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return batch7Lane1S3HTTPError(reply), nil
	}
	return map[string]any{
		"result": "uploaded",
		"key":    key,
		"etag":   batch7Lane1HeaderOrNil(reply.Header, "etag"),
		"size":   len(body),
	}, nil
}

func batch7Lane1S3DeleteObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := stringArg(input, "bucket")
	key := stringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodDelete, bucket, key, nil, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return batch7Lane1S3HTTPError(reply), nil
	}
	return map[string]any{"result": "deleted", "key": key}, nil
}

func batch7Lane1S3CopyObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sourceBucket := stringArg(input, "source_bucket")
	sourceKey := stringArg(input, "source_key")
	destBucket := stringArg(input, "dest_bucket")
	destKey := stringArg(input, "dest_key")
	if sourceBucket == "" || sourceKey == "" || destBucket == "" || destKey == "" {
		return map[string]any{
			"error": "source_bucket, source_key, dest_bucket, and dest_key are required",
		}, nil
	}
	extra := map[string]string{"x-amz-copy-source": "/" + sourceBucket + "/" + sourceKey}
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodPut, destBucket, destKey, nil, nil, extra)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return batch7Lane1S3HTTPError(reply), nil
	}
	return map[string]any{
		"result":      "copied",
		"source":      sourceBucket + "/" + sourceKey,
		"destination": destBucket + "/" + destKey,
	}, nil
}

func batch7Lane1S3GetObjectMetadata(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := stringArg(input, "bucket")
	key := stringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	reply, failure := batch7Lane1S3Request(ctx, rt, http.MethodHead, bucket, key, nil, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status == http.StatusNotFound {
		return map[string]any{"error": "Object not found"}, nil
	}
	if reply.Status >= 400 {
		return map[string]any{"error": fmt.Sprintf("HTTP %d", reply.Status)}, nil
	}
	storageClass := reply.Header.Get("x-amz-storage-class")
	if storageClass == "" {
		storageClass = "STANDARD"
	}
	metadata := map[string]any{
		"key":            key,
		"content_type":   reply.Header.Get("content-type"),
		"content_length": batch7Lane1HeaderOrNil(reply.Header, "content-length"),
		"last_modified":  batch7Lane1HeaderOrNil(reply.Header, "last-modified"),
		"etag":           batch7Lane1HeaderOrNil(reply.Header, "etag"),
		"storage_class":  storageClass,
	}
	for name, values := range reply.Header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-amz-meta-") {
			continue
		}
		metadata["meta_"+lower[len("x-amz-meta-"):]] = strings.Join(values, ", ")
	}
	return metadata, nil
}

func batch7Lane1S3GeneratePresignedURL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := stringArg(input, "bucket")
	key := stringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	creds, authErr := batch7Lane1S3Config(rt)
	if authErr != nil {
		return authErr, nil
	}
	expiresIn := batch7Lane1Clamp(intArg(input, "expires_in", 3600), 1, 604800)

	now := batch7Lane1Now(rt).UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := datestamp + "/" + creds.Region + "/s3/aws4_request"

	base := batch7Lane1S3HostBase(rt, bucket, creds.Region)
	parsed, err := url.Parse(base)
	if err != nil {
		return map[string]any{"error": "S3 request failed: " + err.Error()}, nil
	}
	path := "/" + key
	query := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    creds.AccessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(expiresIn),
		"X-Amz-SignedHeaders": "host",
	}
	canonicalQuery := batch7Lane1S3CanonicalQuery(query)
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		path,
		canonicalQuery,
		"host:" + parsed.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(requestHash[:]),
	}, "\n")
	mac := hmac.New(sha256.New, batch7Lane1S3SignKey(creds.SecretKey, datestamp, creds.Region))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	return map[string]any{
		"url":        base + path + "?" + canonicalQuery + "&X-Amz-Signature=" + signature,
		"expires_in": expiresIn,
		"key":        key,
		"bucket":     bucket,
	}, nil
}

// ----------------------------------------------------------------- Vercel ---

// batch7Lane1VercelCall performs one Vercel API call and maps HTTP failures to
// the upstream `_get` / `_post` error dicts.
func batch7Lane1VercelCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
	payload any,
) (any, bool) {
	token := batch7Lane1ModuleToken(rt, "vercel_tool")
	if token == "" {
		return map[string]any{
			"error": "VERCEL_TOKEN not set",
			"help":  "Get a token at https://vercel.com/account/tokens",
		}, false
	}
	target := batch7Lane1ModuleURL(rt, "vercel_tool") + "/" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	reply, err := batch7Lane1JSON(ctx, rt, method, target, headers, payload)
	if err != nil {
		if batch7Lane1IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Vercel timed out"}, false
		}
		return map[string]any{"error": "Vercel request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your VERCEL_TOKEN."}, false
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden: " + batch7Lane1Truncate(string(reply.Body), 300)}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Vercel API error %d: %s",
				reply.Status,
				batch7Lane1Truncate(string(reply.Body), batch7Lane1MaxError),
			),
		}, false
	}
	value, ok := batch7Lane1DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Vercel request failed: invalid JSON response"}, false
	}
	return value, true
}

func batch7Lane1VercelListDeployments(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit": {strconv.Itoa(batch7Lane1Clamp(intArg(input, "limit", 20), 1, 100))},
	}
	if projectID := stringArg(input, "project_id"); projectID != "" {
		query.Set("projectId", projectID)
	}
	if state := stringArg(input, "state"); state != "" {
		query.Set("state", state)
	}
	value, ok := batch7Lane1VercelCall(ctx, rt, http.MethodGet, "v6/deployments", query, nil)
	if !ok {
		return value, nil
	}
	object := batch7Lane1AsMap(value)
	deployments := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(object["deployments"]) {
		deployment := batch7Lane1AsMap(raw)
		deployments = append(deployments, map[string]any{
			"uid":     batch7Lane1AsString(deployment["uid"]),
			"name":    batch7Lane1AsString(deployment["name"]),
			"url":     batch7Lane1AsString(deployment["url"]),
			"state":   batch7Lane1AsString(deployment["state"]),
			"created": batch7Lane1Number(deployment["created"]),
			"target":  batch7Lane1AsString(deployment["target"]),
		})
	}
	return map[string]any{"deployments": deployments}, nil
}

func batch7Lane1VercelGetDeployment(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	deploymentID := stringArg(input, "deployment_id")
	if deploymentID == "" {
		return map[string]any{"error": "deployment_id is required"}, nil
	}
	value, ok := batch7Lane1VercelCall(
		ctx, rt, http.MethodGet, "v13/deployments/"+deploymentID, nil, nil,
	)
	if !ok {
		return value, nil
	}
	deployment := batch7Lane1AsMap(value)
	return map[string]any{
		"uid":     batch7Lane1AsString(deployment["id"]),
		"name":    batch7Lane1AsString(deployment["name"]),
		"url":     batch7Lane1AsString(deployment["url"]),
		"state":   batch7Lane1AsString(deployment["readyState"]),
		"target":  batch7Lane1AsString(deployment["target"]),
		"created": batch7Lane1Number(deployment["createdAt"]),
		"ready":   batch7Lane1Number(deployment["ready"]),
		"creator": batch7Lane1AsString(batch7Lane1AsMap(deployment["creator"])["username"]),
		"meta":    batch7Lane1AsMap(deployment["meta"]),
	}, nil
}

func batch7Lane1VercelListProjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := url.Values{
		"limit": {strconv.Itoa(batch7Lane1Clamp(intArg(input, "limit", 20), 1, 100))},
	}
	value, ok := batch7Lane1VercelCall(ctx, rt, http.MethodGet, "v9/projects", query, nil)
	if !ok {
		return value, nil
	}
	projects := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["projects"]) {
		project := batch7Lane1AsMap(raw)
		latestURL := ""
		latest := batch7Lane1AsSlice(project["latestDeployments"])
		if len(latest) > 0 {
			latestURL = batch7Lane1AsString(batch7Lane1AsMap(latest[0])["url"])
		}
		projects = append(projects, map[string]any{
			"id":                  batch7Lane1AsString(project["id"]),
			"name":                batch7Lane1AsString(project["name"]),
			"framework":           batch7Lane1AsString(project["framework"]),
			"updatedAt":           batch7Lane1Number(project["updatedAt"]),
			"latestDeploymentUrl": latestURL,
		})
	}
	return map[string]any{"projects": projects}, nil
}

func batch7Lane1VercelGetProject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	value, ok := batch7Lane1VercelCall(ctx, rt, http.MethodGet, "v9/projects/"+projectID, nil, nil)
	if !ok {
		return value, nil
	}
	project := batch7Lane1AsMap(value)
	return map[string]any{
		"id":          batch7Lane1AsString(project["id"]),
		"name":        batch7Lane1AsString(project["name"]),
		"framework":   batch7Lane1AsString(project["framework"]),
		"nodeVersion": batch7Lane1AsString(project["nodeVersion"]),
		"updatedAt":   batch7Lane1Number(project["updatedAt"]),
		"env_count":   len(batch7Lane1AsSlice(project["env"])),
	}, nil
}

func batch7Lane1VercelListProjectDomains(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	value, ok := batch7Lane1VercelCall(
		ctx, rt, http.MethodGet, "v9/projects/"+projectID+"/domains", nil, nil,
	)
	if !ok {
		return value, nil
	}
	domains := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["domains"]) {
		domain := batch7Lane1AsMap(raw)
		domains = append(domains, map[string]any{
			"name":      batch7Lane1AsString(domain["name"]),
			"redirect":  batch7Lane1AsString(domain["redirect"]),
			"gitBranch": batch7Lane1AsString(domain["gitBranch"]),
			"verified":  batch7Lane1AsBool(domain["verified"], false),
		})
	}
	return map[string]any{"project_id": projectID, "domains": domains}, nil
}

func batch7Lane1VercelListEnvVars(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	if projectID == "" {
		return map[string]any{"error": "project_id is required"}, nil
	}
	value, ok := batch7Lane1VercelCall(
		ctx, rt, http.MethodGet, "v9/projects/"+projectID+"/env", nil, nil,
	)
	if !ok {
		return value, nil
	}
	envVars := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["envs"]) {
		envVar := batch7Lane1AsMap(raw)
		envVars = append(envVars, map[string]any{
			"id":     batch7Lane1AsString(envVar["id"]),
			"key":    batch7Lane1AsString(envVar["key"]),
			"target": batch7Lane1StringList(envVar["target"]),
			"type":   batch7Lane1AsString(envVar["type"]),
		})
	}
	return map[string]any{"project_id": projectID, "env_vars": envVars}, nil
}

func batch7Lane1VercelCreateEnvVar(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	projectID := stringArg(input, "project_id")
	key := stringArg(input, "key")
	value := stringArg(input, "value")
	if projectID == "" || key == "" || value == "" {
		return map[string]any{"error": "project_id, key, and value are required"}, nil
	}
	target := stringArg(input, "target")
	if target == "" {
		target = "production,preview,development"
	}
	envType := stringArg(input, "env_type")
	if envType == "" {
		envType = "encrypted"
	}
	targets := make([]string, 0)
	for _, part := range strings.Split(target, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			targets = append(targets, trimmed)
		}
	}
	body := map[string]any{"key": key, "value": value, "target": targets, "type": envType}
	created, ok := batch7Lane1VercelCall(
		ctx, rt, http.MethodPost, "v10/projects/"+projectID+"/env", nil, body,
	)
	if !ok {
		return created, nil
	}
	return map[string]any{
		"id":     batch7Lane1AsString(batch7Lane1AsMap(created)["id"]),
		"key":    key,
		"status": "created",
	}, nil
}

// ------------------------------------------------------------- Docker Hub ---

// batch7Lane1DockerHubCall performs one Docker Hub API call and maps HTTP
// failures to the upstream `_get` / `_delete` error dicts.
func batch7Lane1DockerHubCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
) (any, bool) {
	token := batch7Lane1ModuleToken(rt, "docker_hub_tool")
	if token == "" {
		return map[string]any{
			"error": "DOCKER_HUB_TOKEN not set",
			"help":  "Create a PAT at https://hub.docker.com/settings/security",
		}, false
	}
	target := batch7Lane1ModuleURL(rt, "docker_hub_tool") + "/" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	reply, err := batch7Lane1JSON(ctx, rt, method, target, headers, nil)
	if err != nil {
		if batch7Lane1IsTimeout(ctx, err) {
			return map[string]any{"error": "Request to Docker Hub timed out"}, false
		}
		return map[string]any{"error": "Docker Hub request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your DOCKER_HUB_TOKEN."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Not found"}, false
	}
	if method == http.MethodDelete {
		// _delete treats any success (or empty body) as a deletion.
		if reply.Status == http.StatusNoContent || len(bytes.TrimSpace(reply.Body)) == 0 {
			return map[string]any{"status": "deleted"}, true
		}
		if reply.Status >= 400 {
			return map[string]any{
				"error": fmt.Sprintf(
					"Docker Hub API error %d: %s",
					reply.Status,
					batch7Lane1Truncate(string(reply.Body), batch7Lane1MaxError),
				),
			}, false
		}
		value, ok := batch7Lane1DecodeObject(reply.Body)
		if !ok {
			return map[string]any{"error": "Docker Hub request failed: invalid JSON response"}, false
		}
		return value, true
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"Docker Hub API error %d: %s",
				reply.Status,
				batch7Lane1Truncate(string(reply.Body), batch7Lane1MaxError),
			),
		}, false
	}
	value, ok := batch7Lane1DecodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Docker Hub request failed: invalid JSON response"}, false
	}
	return value, true
}

func batch7Lane1DockerHubSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	values := url.Values{
		"query":     {query},
		"page_size": {strconv.Itoa(batch7Lane1Clamp(intArg(input, "max_results", 25), 1, 100))},
	}
	value, ok := batch7Lane1DockerHubCall(ctx, rt, http.MethodGet, "search/repositories", values)
	if !ok {
		return value, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["results"]) {
		item := batch7Lane1AsMap(raw)
		results = append(results, map[string]any{
			"repo_name":         batch7Lane1AsString(item["repo_name"]),
			"short_description": batch7Lane1AsString(item["short_description"]),
			"star_count":        batch7Lane1IntValue(item["star_count"], 0),
			"is_official":       batch7Lane1AsBool(item["is_official"], false),
			"is_automated":      batch7Lane1AsBool(item["is_automated"], false),
			"pull_count":        batch7Lane1IntValue(item["pull_count"], 0),
		})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func batch7Lane1DockerHubListRepos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	namespace := stringArg(input, "namespace")
	if namespace == "" {
		namespace = strings.TrimSpace(batch7Lane1Getenv(rt)("DOCKER_HUB_USERNAME"))
	}
	if namespace == "" {
		return map[string]any{"error": "namespace is required (or set DOCKER_HUB_USERNAME)"}, nil
	}
	values := url.Values{
		"page_size": {strconv.Itoa(batch7Lane1Clamp(intArg(input, "max_results", 25), 1, 100))},
	}
	value, ok := batch7Lane1DockerHubCall(ctx, rt, http.MethodGet, "repositories/"+namespace, values)
	if !ok {
		return value, nil
	}
	repos := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["results"]) {
		item := batch7Lane1AsMap(raw)
		repos = append(repos, map[string]any{
			"name":         batch7Lane1AsString(item["name"]),
			"namespace":    batch7Lane1AsString(item["namespace"]),
			"description":  batch7Lane1AsString(item["description"]),
			"star_count":   batch7Lane1IntValue(item["star_count"], 0),
			"pull_count":   batch7Lane1IntValue(item["pull_count"], 0),
			"last_updated": batch7Lane1AsString(item["last_updated"]),
			"is_private":   batch7Lane1AsBool(item["is_private"], false),
		})
	}
	return map[string]any{"namespace": namespace, "repos": repos}, nil
}

func batch7Lane1DockerHubListTags(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := stringArg(input, "repository")
	if repository == "" {
		return map[string]any{"error": "repository is required"}, nil
	}
	values := url.Values{
		"page_size": {strconv.Itoa(batch7Lane1Clamp(intArg(input, "max_results", 25), 1, 100))},
		"ordering":  {"-last_updated"},
	}
	value, ok := batch7Lane1DockerHubCall(
		ctx, rt, http.MethodGet, "repositories/"+repository+"/tags", values,
	)
	if !ok {
		return value, nil
	}
	tags := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["results"]) {
		tag := batch7Lane1AsMap(raw)
		digest := ""
		if images := batch7Lane1AsSlice(tag["images"]); len(images) > 0 {
			digest = batch7Lane1AsString(batch7Lane1AsMap(images[0])["digest"])
		}
		tags = append(tags, map[string]any{
			"name":         batch7Lane1AsString(tag["name"]),
			"full_size":    batch7Lane1IntValue(tag["full_size"], 0),
			"last_updated": batch7Lane1AsString(tag["last_updated"]),
			"digest":       digest,
		})
	}
	return map[string]any{"repository": repository, "tags": tags}, nil
}

func batch7Lane1DockerHubGetRepo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := stringArg(input, "repository")
	if repository == "" {
		return map[string]any{"error": "repository is required"}, nil
	}
	value, ok := batch7Lane1DockerHubCall(ctx, rt, http.MethodGet, "repositories/"+repository, nil)
	if !ok {
		return value, nil
	}
	repo := batch7Lane1AsMap(value)
	fullDescription := batch7Lane1AsString(repo["full_description"])
	if len(fullDescription) > 2000 {
		fullDescription = fullDescription[:2000] + "..."
	}
	return map[string]any{
		"name":             batch7Lane1AsString(repo["name"]),
		"namespace":        batch7Lane1AsString(repo["namespace"]),
		"description":      batch7Lane1AsString(repo["description"]),
		"star_count":       batch7Lane1IntValue(repo["star_count"], 0),
		"pull_count":       batch7Lane1IntValue(repo["pull_count"], 0),
		"last_updated":     batch7Lane1AsString(repo["last_updated"]),
		"is_private":       batch7Lane1AsBool(repo["is_private"], false),
		"full_description": fullDescription,
	}, nil
}

func batch7Lane1DockerHubGetTagDetail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := stringArg(input, "repository")
	tag := stringArg(input, "tag")
	if repository == "" || tag == "" {
		return map[string]any{"error": "repository and tag are required"}, nil
	}
	value, ok := batch7Lane1DockerHubCall(
		ctx, rt, http.MethodGet, "repositories/"+repository+"/tags/"+tag, nil,
	)
	if !ok {
		return value, nil
	}
	detail := batch7Lane1AsMap(value)
	images := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(detail["images"]) {
		image := batch7Lane1AsMap(raw)
		images = append(images, map[string]any{
			"architecture": batch7Lane1AsString(image["architecture"]),
			"os":           batch7Lane1AsString(image["os"]),
			"size":         batch7Lane1IntValue(image["size"], 0),
			"digest":       batch7Lane1AsString(image["digest"]),
			"status":       batch7Lane1AsString(image["status"]),
			"last_pushed":  batch7Lane1AsString(image["last_pushed"]),
		})
	}
	name := batch7Lane1AsString(detail["name"])
	if name == "" {
		name = tag
	}
	return map[string]any{
		"repository":            repository,
		"tag":                   name,
		"full_size":             batch7Lane1IntValue(detail["full_size"], 0),
		"last_updated":          batch7Lane1AsString(detail["last_updated"]),
		"last_updater_username": batch7Lane1AsString(detail["last_updater_username"]),
		"images":                images,
		"image_count":           len(images),
	}, nil
}

func batch7Lane1DockerHubDeleteTag(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := stringArg(input, "repository")
	tag := stringArg(input, "tag")
	if repository == "" || tag == "" {
		return map[string]any{"error": "repository and tag are required"}, nil
	}
	value, ok := batch7Lane1DockerHubCall(
		ctx, rt, http.MethodDelete, "repositories/"+repository+"/tags/"+tag, nil,
	)
	if !ok {
		return value, nil
	}
	if status, _ := batch7Lane1AsMap(value)["status"].(string); status == "deleted" {
		return map[string]any{"repository": repository, "tag": tag, "status": "deleted"}, nil
	}
	return map[string]any{"repository": repository, "tag": tag, "status": "deleted"}, nil
}

func batch7Lane1DockerHubListWebhooks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := stringArg(input, "repository")
	if repository == "" {
		return map[string]any{"error": "repository is required"}, nil
	}
	value, ok := batch7Lane1DockerHubCall(
		ctx, rt, http.MethodGet, "repositories/"+repository+"/webhooks", nil,
	)
	if !ok {
		return value, nil
	}
	webhooks := make([]map[string]any, 0)
	for _, raw := range batch7Lane1AsSlice(batch7Lane1AsMap(value)["results"]) {
		webhook := batch7Lane1AsMap(raw)
		urls := make([]string, 0)
		for _, hook := range batch7Lane1AsSlice(webhook["webhooks"]) {
			urls = append(urls, batch7Lane1AsString(batch7Lane1AsMap(hook)["hook_url"]))
		}
		webhooks = append(webhooks, map[string]any{
			"id":                    webhook["id"],
			"name":                  batch7Lane1AsString(webhook["name"]),
			"active":                batch7Lane1AsBool(webhook["active"], false),
			"expect_final_callback": batch7Lane1AsBool(webhook["expect_final_callback"], false),
			"hook_urls":             urls,
			"created_at":            batch7Lane1AsString(webhook["created_date"]),
		})
	}
	return map[string]any{"repository": repository, "webhooks": webhooks, "count": len(webhooks)}, nil
}
