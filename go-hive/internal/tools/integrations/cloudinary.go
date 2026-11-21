// Cloudinary Admin and Upload API (upstream cloudinary_tool, 8 actions).
//
// Credentials are three fields, not one token: CLOUDINARY_CLOUD_NAME,
// CLOUDINARY_API_KEY and CLOUDINARY_API_SECRET. Every request carries the
// Basic auth header the upstream module sends.
//
// Uploads are additionally signed with the Cloudinary SHA1 scheme: take the
// parameters being sent (public_id, folder, tags and the request timestamp),
// sort them by name, join as "k=v" with "&", append the API secret, and hex
// encode the SHA1 digest. The signature, api_key and timestamp travel in the
// same multipart form as the file reference.
//
// Resource paths concatenate public_id verbatim (folder-qualified ids keep
// their slashes), matching the upstream module.
package integrations

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

var batch7Lane9CloudinaryTool = &Tool{
	ID:          "cloudinary_tool",
	Name:        "cloudinary",
	DisplayName: "Cloudinary",
	Summary:     "Image/video upload, management, search and usage (8 actions).",
	Actions: []Action{
		{
			Name:     "cloudinary_upload",
			Summary:  "Upload an image, video, or file to Cloudinary from a URL.",
			Source:   "cloudinary_tool/cloudinary_tool.py",
			Required: []string{"file_url"},
			Params: map[string]any{
				"file_url":      param("string"),
				"public_id":     paramDefault("string", ""),
				"folder":        paramDefault("string", ""),
				"tags":          paramDefault("string", ""),
				"resource_type": paramDefault("string", "auto"),
			},
		},
		{
			Name:    "cloudinary_list_resources",
			Summary: "List resources in your Cloudinary account.",
			Source:  "cloudinary_tool/cloudinary_tool.py",
			Params: map[string]any{
				"resource_type": paramDefault("string", "image"),
				"max_results":   paramDefault("integer", 30),
				"prefix":        paramDefault("string", ""),
			},
		},
		{
			Name:     "cloudinary_get_resource",
			Summary:  "Get details about a specific Cloudinary resource.",
			Source:   "cloudinary_tool/cloudinary_tool.py",
			Required: []string{"public_id"},
			Params: map[string]any{
				"public_id":     param("string"),
				"resource_type": paramDefault("string", "image"),
			},
		},
		{
			Name:     "cloudinary_delete_resource",
			Summary:  "Delete a resource from Cloudinary.",
			Source:   "cloudinary_tool/cloudinary_tool.py",
			Required: []string{"public_id"},
			Params: map[string]any{
				"public_id":     param("string"),
				"resource_type": paramDefault("string", "image"),
			},
		},
		{
			Name:     "cloudinary_search",
			Summary:  "Search for resources using Cloudinary's search API.",
			Source:   "cloudinary_tool/cloudinary_tool.py",
			Required: []string{"expression"},
			Params: map[string]any{
				"expression":  param("string"),
				"max_results": paramDefault("integer", 30),
			},
		},
		{
			Name:    "cloudinary_get_usage",
			Summary: "Get current Cloudinary account usage and limits.",
			Source:  "cloudinary_tool/cloudinary_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "cloudinary_rename_resource",
			Summary:  "Rename a resource in Cloudinary.",
			Source:   "cloudinary_tool/cloudinary_tool.py",
			Required: []string{"from_public_id", "to_public_id"},
			Params: map[string]any{
				"from_public_id": param("string"),
				"to_public_id":   param("string"),
				"resource_type":  paramDefault("string", "image"),
				"overwrite":      paramDefault("boolean", false),
			},
		},
		{
			Name:     "cloudinary_add_tag",
			Summary:  "Add a tag to one or more Cloudinary resources.",
			Source:   "cloudinary_tool/cloudinary_tool.py",
			Required: []string{"tag", "public_ids"},
			Params: map[string]any{
				"tag":           param("string"),
				"public_ids":    param("string"),
				"resource_type": paramDefault("string", "image"),
			},
		},
	},
}

func batch7Lane9CloudinaryImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"cloudinary_upload":          batch7Lane9CloudinaryUpload,
		"cloudinary_list_resources":  batch7Lane9CloudinaryListResources,
		"cloudinary_get_resource":    batch7Lane9CloudinaryGetResource,
		"cloudinary_delete_resource": batch7Lane9CloudinaryDeleteResource,
		"cloudinary_search":          batch7Lane9CloudinarySearch,
		"cloudinary_get_usage":       batch7Lane9CloudinaryUsage,
		"cloudinary_rename_resource": batch7Lane9CloudinaryRename,
		"cloudinary_add_tag":         batch7Lane9CloudinaryAddTag,
	}
}

// batch7Lane9CloudinaryCredentials is the resolved three-field credential set.
type batch7Lane9CloudinaryCredentials struct {
	Cloud  string
	Key    string
	Secret string
}

// batch7Lane9CloudinaryAuthError mirrors upstream _auth_error.
func batch7Lane9CloudinaryAuthError() map[string]any {
	return map[string]any{
		"error": "CLOUDINARY_CLOUD_NAME, CLOUDINARY_API_KEY, and CLOUDINARY_API_SECRET not set",
		"help":  "Get credentials from your Cloudinary dashboard at https://console.cloudinary.com/",
	}
}

// batch7Lane9CloudinaryResolve reads the three credential fields.
func batch7Lane9CloudinaryResolve(rt *Runtime) (batch7Lane9CloudinaryCredentials, map[string]any) {
	creds := batch7Lane9CloudinaryCredentials{
		Cloud:  strings.TrimSpace(runtimeEnv(rt, "CLOUDINARY_CLOUD_NAME")),
		Key:    strings.TrimSpace(runtimeEnv(rt, "CLOUDINARY_API_KEY")),
		Secret: strings.TrimSpace(runtimeEnv(rt, "CLOUDINARY_API_SECRET")),
	}
	if creds.Cloud == "" || creds.Key == "" || creds.Secret == "" {
		return creds, batch7Lane9CloudinaryAuthError()
	}
	return creds, nil
}

// batch7Lane9CloudinaryRoot renders "<base>/<cloud_name>".
func batch7Lane9CloudinaryRoot(rt *Runtime) string {
	base := marketingBase(rt, "cloudinary_tool", batch7Lane9CloudinaryBase)
	if cloud := strings.TrimSpace(runtimeEnv(rt, "CLOUDINARY_CLOUD_NAME")); cloud != "" {
		return base + "/" + url.PathEscape(cloud)
	}
	return base
}

// batch7Lane9CloudinarySignature implements Cloudinary's SHA1 request
// signature: sorted "k=v" pairs joined by "&", then the API secret appended.
// Empty values are skipped, as the provider's SDKs do.
func batch7Lane9CloudinarySignature(params map[string]string, secret string) string {
	names := make([]string, 0, len(params))
	for name, value := range params {
		if value == "" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+params[name])
	}
	digest := sha1.Sum([]byte(strings.Join(parts, "&") + secret))
	return hex.EncodeToString(digest[:])
}

// batch7Lane9CloudinaryCall sends one request and maps provider errors.
func batch7Lane9CloudinaryCall(ctx context.Context, rt *Runtime, creds batch7Lane9CloudinaryCredentials, method, endpoint string, headers map[string]string, body []byte, timeout time.Duration) (map[string]any, map[string]any) {
	if headers == nil {
		headers = map[string]string{}
	}
	headers["Authorization"] = batch7Lane9BasicAuth(creds.Key, creds.Secret)
	resp, err := batch7Lane9Send(ctx, rt, method, endpoint, headers, body, timeout)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request to Cloudinary timed out"}
		}
		return nil, map[string]any{"error": "Cloudinary request failed: " + err.Error()}
	}
	if resp.Status == http.StatusUnauthorized {
		return nil, map[string]any{"error": "Unauthorized. Check your Cloudinary credentials."}
	}
	if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
		return nil, map[string]any{
			"error": "Cloudinary API error " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Cloudinary request failed: invalid JSON response"}
	}
	return data, nil
}

// batch7Lane9CloudinaryForm posts a urlencoded form to the Cloudinary root.
func batch7Lane9CloudinaryForm(ctx context.Context, rt *Runtime, creds batch7Lane9CloudinaryCredentials, path string, form url.Values) (map[string]any, map[string]any) {
	return batch7Lane9CloudinaryCall(ctx, rt, creds, http.MethodPost,
		batch7Lane9CloudinaryRoot(rt)+path, nil, []byte(form.Encode()), 60*time.Second)
}

// batch7Lane9CloudinaryJSON posts a JSON body to the Cloudinary root.
func batch7Lane9CloudinaryJSON(ctx context.Context, rt *Runtime, creds batch7Lane9CloudinaryCredentials, path string, payload any) (map[string]any, map[string]any) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, map[string]any{"error": "Cloudinary request failed: " + err.Error()}
	}
	return batch7Lane9CloudinaryCall(ctx, rt, creds, http.MethodPost,
		batch7Lane9CloudinaryRoot(rt)+path, map[string]string{"Content-Type": "application/json"}, body, 60*time.Second)
}

// batch7Lane9CloudinaryResource renders the shared resource subset.
func batch7Lane9CloudinaryResource(resource map[string]any, withType bool) map[string]any {
	out := map[string]any{
		"public_id":  stringOr(resource, "public_id", ""),
		"secure_url": stringOr(resource, "secure_url", ""),
		"format":     stringOr(resource, "format", ""),
		"bytes":      marketingIntOr(resource, "bytes", 0),
		"width":      resource["width"],
		"height":     resource["height"],
		"created_at": stringOr(resource, "created_at", ""),
	}
	if withType {
		out["resource_type"] = stringOr(resource, "resource_type", "")
	}
	return out
}

func batch7Lane9CloudinaryUpload(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	fileURL := stringArg(input, "file_url")
	if fileURL == "" {
		return map[string]any{"error": "file_url is required"}, nil
	}
	resourceType := stringArg(input, "resource_type")
	if resourceType == "" {
		resourceType = "auto"
	}
	signable := map[string]string{}
	form := url.Values{"file": []string{fileURL}}
	if publicID := stringArg(input, "public_id"); publicID != "" {
		signable["public_id"] = publicID
		form.Set("public_id", publicID)
	}
	if folder := stringArg(input, "folder"); folder != "" {
		signable["folder"] = folder
		form.Set("folder", folder)
	}
	if tags := stringArg(input, "tags"); tags != "" {
		signable["tags"] = tags
		form.Set("tags", tags)
	}
	signable["timestamp"] = strconv.FormatInt(batch7Lane9Timestamp(rt), 10)
	form.Set("timestamp", signable["timestamp"])
	form.Set("api_key", creds.Key)
	form.Set("signature", batch7Lane9CloudinarySignature(signable, creds.Secret))

	data, failure := batch7Lane9CloudinaryForm(ctx, rt, creds, "/"+url.PathEscape(resourceType)+"/upload", form)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{
		"public_id":     stringOr(data, "public_id", ""),
		"secure_url":    stringOr(data, "secure_url", ""),
		"format":        stringOr(data, "format", ""),
		"resource_type": stringOr(data, "resource_type", ""),
		"bytes":         marketingIntOr(data, "bytes", 0),
		"width":         data["width"],
		"height":        data["height"],
		"created_at":    stringOr(data, "created_at", ""),
	}, nil
}

func batch7Lane9CloudinaryListResources(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	resourceType := stringArg(input, "resource_type")
	if resourceType == "" {
		resourceType = "image"
	}
	query := url.Values{
		"max_results": []string{strconv.Itoa(clampInt(intArg(input, "max_results", 30), 1, 500))},
	}
	if prefix := stringArg(input, "prefix"); prefix != "" {
		query.Set("prefix", prefix)
	}
	endpoint := batch7Lane9CloudinaryRoot(rt) + "/resources/" + url.PathEscape(resourceType) + "?" + query.Encode()
	data, failure := batch7Lane9CloudinaryCall(ctx, rt, creds, http.MethodGet, endpoint, nil, nil, 60*time.Second)
	if failure != nil {
		return failure, nil
	}
	resources := make([]any, 0)
	for _, raw := range asSlice(data["resources"]) {
		resources = append(resources, batch7Lane9CloudinaryResource(asMap(raw), false))
	}
	return map[string]any{"resources": resources, "count": len(resources)}, nil
}

func batch7Lane9CloudinaryGetResource(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	publicID := stringArg(input, "public_id")
	if publicID == "" {
		return map[string]any{"error": "public_id is required"}, nil
	}
	resourceType := stringArg(input, "resource_type")
	if resourceType == "" {
		resourceType = "image"
	}
	endpoint := batch7Lane9CloudinaryRoot(rt) + "/resources/" + url.PathEscape(resourceType) + "/upload/" + publicID
	data, failure := batch7Lane9CloudinaryCall(ctx, rt, creds, http.MethodGet, endpoint, nil, nil, 60*time.Second)
	if failure != nil {
		return failure, nil
	}
	tags := asSlice(data["tags"])
	if tags == nil {
		tags = []any{}
	}
	return map[string]any{
		"public_id":     stringOr(data, "public_id", ""),
		"secure_url":    stringOr(data, "secure_url", ""),
		"format":        stringOr(data, "format", ""),
		"resource_type": stringOr(data, "resource_type", ""),
		"bytes":         marketingIntOr(data, "bytes", 0),
		"width":         data["width"],
		"height":        data["height"],
		"tags":          tags,
		"created_at":    stringOr(data, "created_at", ""),
		"status":        stringOr(data, "status", ""),
	}, nil
}

func batch7Lane9CloudinaryDeleteResource(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	publicID := stringArg(input, "public_id")
	if publicID == "" {
		return map[string]any{"error": "public_id is required"}, nil
	}
	resourceType := stringArg(input, "resource_type")
	if resourceType == "" {
		resourceType = "image"
	}
	form := url.Values{"public_id": []string{publicID}}
	data, failure := batch7Lane9CloudinaryForm(ctx, rt, creds, "/"+url.PathEscape(resourceType)+"/destroy", form)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{
		"public_id": publicID,
		"result":    stringOr(data, "result", "unknown"),
	}, nil
}

func batch7Lane9CloudinarySearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	expression := stringArg(input, "expression")
	if expression == "" {
		return map[string]any{"error": "expression is required"}, nil
	}
	payload := map[string]any{
		"expression":  expression,
		"max_results": clampInt(intArg(input, "max_results", 30), 1, 500),
	}
	data, failure := batch7Lane9CloudinaryJSON(ctx, rt, creds, "/resources/search", payload)
	if failure != nil {
		return failure, nil
	}
	resources := make([]any, 0)
	for _, raw := range asSlice(data["resources"]) {
		resource := asMap(raw)
		resources = append(resources, map[string]any{
			"public_id":     stringOr(resource, "public_id", ""),
			"secure_url":    stringOr(resource, "secure_url", ""),
			"format":        stringOr(resource, "format", ""),
			"resource_type": stringOr(resource, "resource_type", ""),
			"bytes":         marketingIntOr(resource, "bytes", 0),
			"created_at":    stringOr(resource, "created_at", ""),
		})
	}
	return map[string]any{
		"resources":   resources,
		"total_count": marketingIntOr(data, "total_count", 0),
	}, nil
}

func batch7Lane9CloudinaryUsage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	data, failure := batch7Lane9CloudinaryCall(ctx, rt, creds, http.MethodGet,
		batch7Lane9CloudinaryRoot(rt)+"/usage", nil, nil, 60*time.Second)
	if failure != nil {
		return failure, nil
	}
	storage := asMap(data["storage"])
	bandwidth := asMap(data["bandwidth"])
	transformations := asMap(data["transformations"])
	return map[string]any{
		"plan": stringOr(data, "plan", ""),
		"storage": map[string]any{
			"used_bytes":   marketingIntOr(storage, "usage", 0),
			"limit_bytes":  marketingIntOr(storage, "limit", 0),
			"used_percent": marketingFloatOr(storage, "used_percent", 0),
		},
		"bandwidth": map[string]any{
			"used_bytes":   marketingIntOr(bandwidth, "usage", 0),
			"limit_bytes":  marketingIntOr(bandwidth, "limit", 0),
			"used_percent": marketingFloatOr(bandwidth, "used_percent", 0),
		},
		"transformations": map[string]any{
			"used":         marketingIntOr(transformations, "usage", 0),
			"limit":        marketingIntOr(transformations, "limit", 0),
			"used_percent": marketingFloatOr(transformations, "used_percent", 0),
		},
		"resources":         marketingIntOr(data, "resources", 0),
		"derived_resources": marketingIntOr(data, "derived_resources", 0),
		"last_updated":      stringOr(data, "last_updated", ""),
	}, nil
}

func batch7Lane9CloudinaryRename(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	fromID, toID := stringArg(input, "from_public_id"), stringArg(input, "to_public_id")
	if fromID == "" || toID == "" {
		return map[string]any{"error": "from_public_id and to_public_id are required"}, nil
	}
	resourceType := stringArg(input, "resource_type")
	if resourceType == "" {
		resourceType = "image"
	}
	form := url.Values{
		"from_public_id": []string{fromID},
		"to_public_id":   []string{toID},
	}
	if boolArg(input, "overwrite", false) {
		form.Set("overwrite", "true")
	}
	data, failure := batch7Lane9CloudinaryForm(ctx, rt, creds, "/"+url.PathEscape(resourceType)+"/rename", form)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{
		"public_id":  stringOr(data, "public_id", ""),
		"secure_url": stringOr(data, "secure_url", ""),
		"format":     stringOr(data, "format", ""),
		"status":     "renamed",
	}, nil
}

func batch7Lane9CloudinaryAddTag(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	creds, failure := batch7Lane9CloudinaryResolve(rt)
	if failure != nil {
		return failure, nil
	}
	tag, publicIDs := stringArg(input, "tag"), stringArg(input, "public_ids")
	if tag == "" || publicIDs == "" {
		return map[string]any{"error": "tag and public_ids are required"}, nil
	}
	resourceType := stringArg(input, "resource_type")
	if resourceType == "" {
		resourceType = "image"
	}
	ids := make([]any, 0)
	for _, part := range strings.Split(publicIDs, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			ids = append(ids, trimmed)
		}
	}
	payload := map[string]any{
		"tag":        tag,
		"public_ids": ids,
		"command":    "add",
	}
	data, failure := batch7Lane9CloudinaryJSON(ctx, rt, creds, "/"+url.PathEscape(resourceType)+"/tags", payload)
	if failure != nil {
		return failure, nil
	}
	tagged := asSlice(data["public_ids"])
	if tagged == nil {
		tagged = ids
	}
	return map[string]any{
		"tag":        tag,
		"public_ids": tagged,
		"status":     "tagged",
	}, nil
}
