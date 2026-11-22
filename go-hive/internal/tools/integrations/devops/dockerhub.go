package devops

// Docker Hub: catalog, seven registry actions and the shared call helper.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// dockerHubTool publishes the seven Docker Hub registry actions.
var dockerHubTool = &Tool{
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
				"query":       integrationkit.Param("string"),
				"max_results": integrationkit.ParamDefault("integer", 25),
			},
		},
		{
			Name:    "docker_hub_list_repos",
			Summary: "List repositories for a Docker Hub user or organization.",
			Source:  "docker_hub_tool/docker_hub_tool.py",
			Params: map[string]any{
				"namespace":   integrationkit.ParamDefault("string", ""),
				"max_results": integrationkit.ParamDefault("integer", 25),
			},
		},
		{
			Name:     "docker_hub_list_tags",
			Summary:  "List tags for a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository"},
			Params: map[string]any{
				"repository":  integrationkit.Param("string"),
				"max_results": integrationkit.ParamDefault("integer", 25),
			},
		},
		{
			Name:     "docker_hub_get_repo",
			Summary:  "Get detailed information about a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository"},
			Params: map[string]any{
				"repository": integrationkit.Param("string"),
			},
		},
		{
			Name:     "docker_hub_get_tag_detail",
			Summary:  "Get detailed information about a specific image tag.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository", "tag"},
			Params: map[string]any{
				"repository": integrationkit.Param("string"),
				"tag":        integrationkit.Param("string"),
			},
		},
		{
			Name:     "docker_hub_delete_tag",
			Summary:  "Delete a specific tag from a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository", "tag"},
			Params: map[string]any{
				"repository": integrationkit.Param("string"),
				"tag":        integrationkit.Param("string"),
			},
		},
		{
			Name:     "docker_hub_list_webhooks",
			Summary:  "List webhooks configured for a Docker Hub repository.",
			Source:   "docker_hub_tool/docker_hub_tool.py",
			Required: []string{"repository"},
			Params: map[string]any{
				"repository": integrationkit.Param("string"),
			},
		},
	},
}

func dockerHubImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"docker_hub_search":         dockerHubSearch,
		"docker_hub_list_repos":     dockerHubListRepos,
		"docker_hub_list_tags":      dockerHubListTags,
		"docker_hub_get_repo":       dockerHubGetRepo,
		"docker_hub_get_tag_detail": dockerHubGetTagDetail,
		"docker_hub_delete_tag":     dockerHubDeleteTag,
		"docker_hub_list_webhooks":  dockerHubListWebhooks,
	}
}

// ------------------------------------------------------------- Docker Hub ---

// dockerHubCall performs one Docker Hub API call and maps HTTP
// failures to the upstream `_get` / `_delete` error dicts.
func dockerHubCall(
	ctx context.Context,
	rt *Runtime,
	method, path string,
	query url.Values,
) (any, bool) {
	token := moduleToken(rt, "docker_hub_tool")
	if token == "" {
		return map[string]any{
			"error": "DOCKER_HUB_TOKEN not set",
			"help":  "Create a PAT at https://hub.docker.com/settings/security",
		}, false
	}
	target := moduleURL(rt, "docker_hub_tool") + "/" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
	reply, err := jsonRequest(ctx, rt, method, target, headers, nil)
	if err != nil {
		if isTimeout(ctx, err) {
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
					truncateText(string(reply.Body), maxErrorBytes),
				),
			}, false
		}
		value, ok := decodeObject(reply.Body)
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
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Docker Hub request failed: invalid JSON response"}, false
	}
	return value, true
}

func dockerHubSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := integrationkit.StringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	values := url.Values{
		"query":     {query},
		"page_size": {strconv.Itoa(clampInt(integrationkit.IntArg(input, "max_results", 25), 1, 100))},
	}
	value, ok := dockerHubCall(ctx, rt, http.MethodGet, "search/repositories", values)
	if !ok {
		return value, nil
	}
	results := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["results"]) {
		item := asMap(raw)
		results = append(results, map[string]any{
			"repo_name":         asString(item["repo_name"]),
			"short_description": asString(item["short_description"]),
			"star_count":        intValue(item["star_count"], 0),
			"is_official":       asBool(item["is_official"], false),
			"is_automated":      asBool(item["is_automated"], false),
			"pull_count":        intValue(item["pull_count"], 0),
		})
	}
	return map[string]any{"query": query, "results": results}, nil
}

func dockerHubListRepos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	namespace := integrationkit.StringArg(input, "namespace")
	if namespace == "" {
		namespace = strings.TrimSpace(runtimeGetenv(rt)("DOCKER_HUB_USERNAME"))
	}
	if namespace == "" {
		return map[string]any{"error": "namespace is required (or set DOCKER_HUB_USERNAME)"}, nil
	}
	values := url.Values{
		"page_size": {strconv.Itoa(clampInt(integrationkit.IntArg(input, "max_results", 25), 1, 100))},
	}
	value, ok := dockerHubCall(ctx, rt, http.MethodGet, "repositories/"+namespace, values)
	if !ok {
		return value, nil
	}
	repos := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["results"]) {
		item := asMap(raw)
		repos = append(repos, map[string]any{
			"name":         asString(item["name"]),
			"namespace":    asString(item["namespace"]),
			"description":  asString(item["description"]),
			"star_count":   intValue(item["star_count"], 0),
			"pull_count":   intValue(item["pull_count"], 0),
			"last_updated": asString(item["last_updated"]),
			"is_private":   asBool(item["is_private"], false),
		})
	}
	return map[string]any{"namespace": namespace, "repos": repos}, nil
}

func dockerHubListTags(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := integrationkit.StringArg(input, "repository")
	if repository == "" {
		return map[string]any{"error": "repository is required"}, nil
	}
	values := url.Values{
		"page_size": {strconv.Itoa(clampInt(integrationkit.IntArg(input, "max_results", 25), 1, 100))},
		"ordering":  {"-last_updated"},
	}
	value, ok := dockerHubCall(
		ctx, rt, http.MethodGet, "repositories/"+repository+"/tags", values,
	)
	if !ok {
		return value, nil
	}
	tags := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["results"]) {
		tag := asMap(raw)
		digest := ""
		if images := asSlice(tag["images"]); len(images) > 0 {
			digest = asString(asMap(images[0])["digest"])
		}
		tags = append(tags, map[string]any{
			"name":         asString(tag["name"]),
			"full_size":    intValue(tag["full_size"], 0),
			"last_updated": asString(tag["last_updated"]),
			"digest":       digest,
		})
	}
	return map[string]any{"repository": repository, "tags": tags}, nil
}

func dockerHubGetRepo(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := integrationkit.StringArg(input, "repository")
	if repository == "" {
		return map[string]any{"error": "repository is required"}, nil
	}
	value, ok := dockerHubCall(ctx, rt, http.MethodGet, "repositories/"+repository, nil)
	if !ok {
		return value, nil
	}
	repo := asMap(value)
	fullDescription := asString(repo["full_description"])
	if len(fullDescription) > 2000 {
		fullDescription = fullDescription[:2000] + "..."
	}
	return map[string]any{
		"name":             asString(repo["name"]),
		"namespace":        asString(repo["namespace"]),
		"description":      asString(repo["description"]),
		"star_count":       intValue(repo["star_count"], 0),
		"pull_count":       intValue(repo["pull_count"], 0),
		"last_updated":     asString(repo["last_updated"]),
		"is_private":       asBool(repo["is_private"], false),
		"full_description": fullDescription,
	}, nil
}

func dockerHubGetTagDetail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := integrationkit.StringArg(input, "repository")
	tag := integrationkit.StringArg(input, "tag")
	if repository == "" || tag == "" {
		return map[string]any{"error": "repository and tag are required"}, nil
	}
	value, ok := dockerHubCall(
		ctx, rt, http.MethodGet, "repositories/"+repository+"/tags/"+tag, nil,
	)
	if !ok {
		return value, nil
	}
	detail := asMap(value)
	images := make([]map[string]any, 0)
	for _, raw := range asSlice(detail["images"]) {
		image := asMap(raw)
		images = append(images, map[string]any{
			"architecture": asString(image["architecture"]),
			"os":           asString(image["os"]),
			"size":         intValue(image["size"], 0),
			"digest":       asString(image["digest"]),
			"status":       asString(image["status"]),
			"last_pushed":  asString(image["last_pushed"]),
		})
	}
	name := asString(detail["name"])
	if name == "" {
		name = tag
	}
	return map[string]any{
		"repository":            repository,
		"tag":                   name,
		"full_size":             intValue(detail["full_size"], 0),
		"last_updated":          asString(detail["last_updated"]),
		"last_updater_username": asString(detail["last_updater_username"]),
		"images":                images,
		"image_count":           len(images),
	}, nil
}

func dockerHubDeleteTag(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := integrationkit.StringArg(input, "repository")
	tag := integrationkit.StringArg(input, "tag")
	if repository == "" || tag == "" {
		return map[string]any{"error": "repository and tag are required"}, nil
	}
	value, ok := dockerHubCall(
		ctx, rt, http.MethodDelete, "repositories/"+repository+"/tags/"+tag, nil,
	)
	if !ok {
		return value, nil
	}
	if status, _ := asMap(value)["status"].(string); status == "deleted" {
		return map[string]any{"repository": repository, "tag": tag, "status": "deleted"}, nil
	}
	return map[string]any{"repository": repository, "tag": tag, "status": "deleted"}, nil
}

func dockerHubListWebhooks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	repository := integrationkit.StringArg(input, "repository")
	if repository == "" {
		return map[string]any{"error": "repository is required"}, nil
	}
	value, ok := dockerHubCall(
		ctx, rt, http.MethodGet, "repositories/"+repository+"/webhooks", nil,
	)
	if !ok {
		return value, nil
	}
	webhooks := make([]map[string]any, 0)
	for _, raw := range asSlice(asMap(value)["results"]) {
		webhook := asMap(raw)
		urls := make([]string, 0)
		for _, hook := range asSlice(webhook["webhooks"]) {
			urls = append(urls, asString(asMap(hook)["hook_url"]))
		}
		webhooks = append(webhooks, map[string]any{
			"id":                    webhook["id"],
			"name":                  asString(webhook["name"]),
			"active":                asBool(webhook["active"], false),
			"expect_final_callback": asBool(webhook["expect_final_callback"], false),
			"hook_urls":             urls,
			"created_at":            asString(webhook["created_date"]),
		})
	}
	return map[string]any{"repository": repository, "webhooks": webhooks, "count": len(webhooks)}, nil
}
