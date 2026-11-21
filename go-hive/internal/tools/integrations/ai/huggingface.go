package ai

// Hugging Face Hub discovery plus the Inference API and Inference Endpoints.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ------------------------------------------------------------- huggingface ---

var huggingFaceTool = &Tool{
	ID:          "huggingface_tool",
	Name:        "huggingface",
	DisplayName: "Hugging Face",
	Summary:     "Hugging Face Hub discovery plus Inference API calls (9 actions).",
	Actions: []Action{
		{
			Name:    "huggingface_search_models",
			Summary: "Search for models on HuggingFace Hub.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"query":  paramDefault("string", ""),
				"author": paramDefault("string", ""),
				"sort":   paramDefault("string", "downloads"),
				"limit":  paramDefault("integer", 20),
			},
		},
		{
			Name:     "huggingface_get_model",
			Summary:  "Get details about a specific model on HuggingFace Hub.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"model_id"},
			Params: map[string]any{
				"model_id": param("string"),
			},
		},
		{
			Name:    "huggingface_search_datasets",
			Summary: "Search for datasets on HuggingFace Hub.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"query":  paramDefault("string", ""),
				"author": paramDefault("string", ""),
				"sort":   paramDefault("string", "downloads"),
				"limit":  paramDefault("integer", 20),
			},
		},
		{
			Name:     "huggingface_get_dataset",
			Summary:  "Get details about a specific dataset on HuggingFace Hub.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"dataset_id"},
			Params: map[string]any{
				"dataset_id": param("string"),
			},
		},
		{
			Name:    "huggingface_search_spaces",
			Summary: "Search for Spaces on HuggingFace Hub.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"query":  paramDefault("string", ""),
				"author": paramDefault("string", ""),
				"sort":   paramDefault("string", "likes"),
				"limit":  paramDefault("integer", 20),
			},
		},
		{
			Name:    "huggingface_whoami",
			Summary: "Get info about the authenticated HuggingFace user.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "huggingface_run_inference",
			Summary:  "Run inference on a HuggingFace model via the Inference API.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"model_id", "inputs"},
			Params: map[string]any{
				"model_id":   param("string"),
				"inputs":     param("string"),
				"task":       paramDefault("string", ""),
				"parameters": paramDefault("string", ""),
			},
		},
		{
			Name:     "huggingface_run_embedding",
			Summary:  "Generate text embeddings using a HuggingFace model via the Inference API.",
			Source:   "huggingface_tool/huggingface_tool.py",
			Required: []string{"model_id", "inputs"},
			Params: map[string]any{
				"model_id": param("string"),
				"inputs":   param("string"),
			},
		},
		{
			Name:    "huggingface_list_inference_endpoints",
			Summary: "List deployed Inference Endpoints on HuggingFace.",
			Source:  "huggingface_tool/huggingface_tool.py",
			Params: map[string]any{
				"namespace": paramDefault("string", ""),
			},
		},
	},
}

func huggingFaceImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"huggingface_search_models":            huggingFaceSearchModels,
		"huggingface_get_model":                huggingFaceGetModel,
		"huggingface_search_datasets":          huggingFaceSearchDatasets,
		"huggingface_get_dataset":              huggingFaceGetDataset,
		"huggingface_search_spaces":            huggingFaceSearchSpaces,
		"huggingface_whoami":                   huggingFaceWhoami,
		"huggingface_run_inference":            huggingFaceRunInference,
		"huggingface_run_embedding":            huggingFaceRunEmbedding,
		"huggingface_list_inference_endpoints": huggingFaceListInferenceEndpoints,
	}
}

// huggingFaceAuth returns the token or the upstream auth error.
func huggingFaceAuth(rt *Runtime) (string, map[string]any) {
	token := moduleToken(rt, "huggingface_tool")
	if token == "" {
		return "", map[string]any{
			"error": "HUGGINGFACE_TOKEN not set",
			"help":  "Get a token at https://huggingface.co/settings/tokens",
		}
	}
	return token, nil
}

// huggingFaceGet performs one Hub API GET. The boolean reports
// success and the value keeps the upstream list/dict distinction.
func huggingFaceGet(
	ctx context.Context,
	rt *Runtime,
	token, path string,
	params url.Values,
) (any, bool) {
	target := moduleBaseURL(rt, "huggingface_tool")
	if target == "" {
		target = huggingFaceBase
	}
	target += path
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	reply, err := doRequest(ctx, rt, http.MethodGet, target, headers, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to HuggingFace timed out"}, false
		}
		return map[string]any{"error": "HuggingFace request failed: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your HUGGINGFACE_TOKEN."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Not found: " + path}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"HuggingFace API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "HuggingFace request failed: invalid JSON response"}, false
	}
	return value, true
}

// huggingFaceInferencePOST calls the Inference API for one model.
func huggingFaceInferencePOST(
	ctx context.Context,
	rt *Runtime,
	token, modelID string,
	payload map[string]any,
) (any, bool) {
	base := moduleBaseURL(rt, "huggingface_inference_tool")
	if base == "" {
		base = huggingFaceInferenceBase
	}
	target := base + "/" + modelID
	headers := map[string]string{"Content-Type": "application/json"}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	reply, err := doRequest(ctx, rt, http.MethodPost, target, headers, payload)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{
				"error": "Inference request timed out. Try a smaller input or a faster model.",
			}, false
		}
		return map[string]any{
			"error": "HuggingFace inference request failed: " + err.Error(),
		}, false
	}
	switch reply.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your HUGGINGFACE_TOKEN."}, false
	case http.StatusNotFound:
		return map[string]any{"error": "Model not found: " + target}, false
	case http.StatusServiceUnavailable:
		estimated := any("unknown")
		if strings.HasPrefix(strings.ToLower(reply.ContentType), "application/json") {
			if body, ok := decodeObject(reply.Body); ok {
				if value, present := body["estimated_time"]; present && value != nil {
					estimated = value
				}
			}
		}
		return map[string]any{
			"error":          "Model is loading",
			"estimated_time": estimated,
			"help":           "The model is being loaded. Retry after the estimated time.",
		}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"HuggingFace Inference API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{
			"error": "HuggingFace inference request failed: invalid JSON response",
		}, false
	}
	return value, true
}

// huggingFaceListEndpoints calls the Inference Endpoints API.
func huggingFaceListEndpoints(
	ctx context.Context,
	rt *Runtime,
	token, path string,
) (any, bool) {
	base := moduleBaseURL(rt, "huggingface_endpoints_tool")
	if base == "" {
		base = huggingFaceEndpointsBase
	}
	reply, err := doRequest(
		ctx, rt, http.MethodGet, base+path,
		map[string]string{"Authorization": "Bearer " + token}, nil,
	)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to HuggingFace Endpoints API timed out"}, false
		}
		return map[string]any{"error": "Endpoints request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your HUGGINGFACE_TOKEN."}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf(
				"Failed to list endpoints (HTTP %d): %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	value, ok := decodeAny(reply.Body)
	if !ok {
		return map[string]any{"error": "Endpoints request failed: invalid JSON response"}, false
	}
	return value, true
}

// huggingFaceSearchParams mirrors the shared search parameter block.
func huggingFaceSearchParams(input map[string]any, defaultSort string) url.Values {
	params := url.Values{
		"sort":      {stringDefault(input["sort"], defaultSort)},
		"direction": {"-1"},
		"limit":     {strconv.Itoa(clampInt(intArg(input, "limit", 20), 1, 100))},
	}
	if query := stringArg(input, "query"); query != "" {
		params.Set("search", query)
	}
	if author := stringArg(input, "author"); author != "" {
		params.Set("author", author)
	}
	return params
}

func huggingFaceSearchModels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := huggingFaceGet(
		ctx, rt, token, "/models", huggingFaceSearchParams(input, "downloads"),
	)
	if !ok {
		return data, nil
	}
	models := make([]map[string]any, 0)
	for _, raw := range asSlice(data) {
		model := asMap(raw)
		models = append(models, map[string]any{
			"id":            asString(model["id"]),
			"author":        asString(model["author"]),
			"downloads":     jsonNumber(intOr(model["downloads"], 0)),
			"likes":         jsonNumber(intOr(model["likes"], 0)),
			"pipeline_tag":  asString(model["pipeline_tag"]),
			"tags":          firstN(stringList(model["tags"]), 10),
			"last_modified": asString(model["lastModified"]),
		})
	}
	return map[string]any{"models": models, "count": len(models)}, nil
}

func huggingFaceGetModel(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	modelID := stringArg(input, "model_id")
	if modelID == "" {
		return map[string]any{"error": "model_id is required"}, nil
	}
	data, ok := huggingFaceGet(ctx, rt, token, "/models/"+modelID, nil)
	if !ok {
		return data, nil
	}
	model := asMap(data)
	return map[string]any{
		"id":            asString(model["id"]),
		"author":        asString(model["author"]),
		"downloads":     jsonNumber(intOr(model["downloads"], 0)),
		"likes":         jsonNumber(intOr(model["likes"], 0)),
		"pipeline_tag":  asString(model["pipeline_tag"]),
		"tags":          stringList(model["tags"]),
		"library_name":  asString(model["library_name"]),
		"model_index":   model["model-index"],
		"card_data":     model["cardData"],
		"private":       boolOr(model["private"], false),
		"last_modified": asString(model["lastModified"]),
		"created_at":    asString(model["createdAt"]),
	}, nil
}

func huggingFaceSearchDatasets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := huggingFaceGet(
		ctx, rt, token, "/datasets", huggingFaceSearchParams(input, "downloads"),
	)
	if !ok {
		return data, nil
	}
	datasets := make([]map[string]any, 0)
	for _, raw := range asSlice(data) {
		dataset := asMap(raw)
		datasets = append(datasets, map[string]any{
			"id":            asString(dataset["id"]),
			"author":        asString(dataset["author"]),
			"downloads":     jsonNumber(intOr(dataset["downloads"], 0)),
			"likes":         jsonNumber(intOr(dataset["likes"], 0)),
			"tags":          firstN(stringList(dataset["tags"]), 10),
			"last_modified": asString(dataset["lastModified"]),
		})
	}
	return map[string]any{"datasets": datasets, "count": len(datasets)}, nil
}

func huggingFaceGetDataset(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	datasetID := stringArg(input, "dataset_id")
	if datasetID == "" {
		return map[string]any{"error": "dataset_id is required"}, nil
	}
	data, ok := huggingFaceGet(ctx, rt, token, "/datasets/"+datasetID, nil)
	if !ok {
		return data, nil
	}
	dataset := asMap(data)
	return map[string]any{
		"id":            asString(dataset["id"]),
		"author":        asString(dataset["author"]),
		"downloads":     jsonNumber(intOr(dataset["downloads"], 0)),
		"likes":         jsonNumber(intOr(dataset["likes"], 0)),
		"tags":          stringList(dataset["tags"]),
		"card_data":     dataset["cardData"],
		"private":       boolOr(dataset["private"], false),
		"last_modified": asString(dataset["lastModified"]),
		"created_at":    asString(dataset["createdAt"]),
	}, nil
}

func huggingFaceSearchSpaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := huggingFaceGet(
		ctx, rt, token, "/spaces", huggingFaceSearchParams(input, "likes"),
	)
	if !ok {
		return data, nil
	}
	spaces := make([]map[string]any, 0)
	for _, raw := range asSlice(data) {
		space := asMap(raw)
		spaces = append(spaces, map[string]any{
			"id":            asString(space["id"]),
			"author":        asString(space["author"]),
			"likes":         jsonNumber(intOr(space["likes"], 0)),
			"sdk":           asString(space["sdk"]),
			"tags":          firstN(stringList(space["tags"]), 10),
			"last_modified": asString(space["lastModified"]),
		})
	}
	return map[string]any{"spaces": spaces, "count": len(spaces)}, nil
}

func huggingFaceWhoami(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := huggingFaceGet(ctx, rt, token, "/whoami-v2", nil)
	if !ok {
		return data, nil
	}
	user := asMap(data)
	orgs := make([]map[string]any, 0)
	for _, raw := range asSlice(user["orgs"]) {
		org := asMap(raw)
		orgs = append(orgs, map[string]any{
			"name": asString(org["name"]),
			"role": asString(org["roleInOrg"]),
		})
	}
	return map[string]any{
		"name":       asString(user["name"]),
		"fullname":   asString(user["fullname"]),
		"email":      asString(user["email"]),
		"avatar_url": asString(user["avatarUrl"]),
		"orgs":       orgs,
		"type":       asString(user["type"]),
	}, nil
}

func huggingFaceRunInference(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	modelID := stringArg(input, "model_id")
	if modelID == "" {
		return map[string]any{"error": "model_id is required"}, nil
	}
	inputs := stringArg(input, "inputs")
	if inputs == "" {
		return map[string]any{"error": "inputs is required"}, nil
	}
	payload := map[string]any{"inputs": inputs}
	if raw := stringArg(input, "parameters"); raw != "" {
		parameters, ok := decodeAny([]byte(raw))
		if !ok {
			return map[string]any{"error": "parameters must be a valid JSON string"}, nil
		}
		payload["parameters"] = parameters
	}
	data, ok := huggingFaceInferencePOST(ctx, rt, token, modelID, payload)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"model_id": modelID,
		"task":     stringDefault(input["task"], "auto"),
		"output":   data,
	}, nil
}

func huggingFaceRunEmbedding(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	modelID := stringArg(input, "model_id")
	if modelID == "" {
		return map[string]any{"error": "model_id is required"}, nil
	}
	inputs := stringArg(input, "inputs")
	if inputs == "" {
		return map[string]any{"error": "inputs is required"}, nil
	}
	data, ok := huggingFaceInferencePOST(
		ctx, rt, token, modelID, map[string]any{"inputs": inputs},
	)
	if !ok {
		return data, nil
	}
	embedding := asSlice(data)
	dims := 0
	if len(embedding) > 0 {
		switch embedding[0].(type) {
		case float64, int, int64:
			dims = len(embedding)
		}
	}
	return map[string]any{
		"model_id":   modelID,
		"embedding":  embedding,
		"dimensions": dims,
	}, nil
}

func huggingFaceListInferenceEndpoints(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := huggingFaceAuth(rt)
	if problem != nil {
		return problem, nil
	}
	path := "/api/endpoints"
	if namespace := stringArg(input, "namespace"); namespace != "" {
		path += "/" + namespace
	}
	data, ok := huggingFaceListEndpoints(ctx, rt, token, path)
	if !ok {
		return data, nil
	}
	items := data
	if object, isObject := data.(map[string]any); isObject {
		if nested, present := object["items"]; present && nested != nil {
			items = nested
		}
	}
	endpoints := make([]map[string]any, 0)
	for _, raw := range asSlice(items) {
		endpoint := asMap(raw)
		model := ""
		if modelObject, ok := endpoint["model"].(map[string]any); ok {
			model = asString(modelObject["repository"])
		} else {
			model = asString(endpoint["model"])
		}
		status := ""
		endpointURL := ""
		if statusObject, ok := endpoint["status"].(map[string]any); ok {
			status = asString(statusObject["state"])
			endpointURL = asString(statusObject["url"])
		} else {
			status = asString(endpoint["status"])
		}
		provider := ""
		region := ""
		if providerObject, ok := endpoint["provider"].(map[string]any); ok {
			provider = asString(providerObject["vendor"])
			region = asString(providerObject["region"])
		}
		endpoints = append(endpoints, map[string]any{
			"name":     asString(endpoint["name"]),
			"model":    model,
			"status":   status,
			"url":      endpointURL,
			"type":     asString(endpoint["type"]),
			"provider": provider,
			"region":   region,
		})
	}
	return map[string]any{"endpoints": endpoints, "count": len(endpoints)}, nil
}
