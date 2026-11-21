package ai

// Pinecone vector database: the control plane (indexes) and the per-index
// data plane (vectors). Ported from batch-7 lane-4 ai_platform.go; the
// model-facing catalog is unchanged.

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ---------------------------------------------------------------- pinecone ---

var pineconeTool = &Tool{
	ID:          "pinecone_tool",
	Name:        "pinecone",
	DisplayName: "Pinecone",
	Summary:     "Pinecone vector database: indexes and vector operations (9 actions).",
	Actions: []Action{
		{
			Name:    "pinecone_list_indexes",
			Summary: "List all indexes in your Pinecone project.",
			Source:  "pinecone_tool/pinecone_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "pinecone_create_index",
			Summary:  "Create a new serverless Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"name", "dimension"},
			Params: map[string]any{
				"name":      param("string"),
				"dimension": param("integer"),
				"metric":    paramDefault("string", "cosine"),
				"cloud":     paramDefault("string", "aws"),
				"region":    paramDefault("string", "us-east-1"),
			},
		},
		{
			Name:     "pinecone_describe_index",
			Summary:  "Get details about a specific Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_name"},
			Params: map[string]any{
				"index_name": param("string"),
			},
		},
		{
			Name:     "pinecone_delete_index",
			Summary:  "Delete a Pinecone index. This is irreversible.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_name"},
			Params: map[string]any{
				"index_name": param("string"),
			},
		},
		{
			Name:     "pinecone_upsert_vectors",
			Summary:  "Upsert vectors into a Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host", "vectors"},
			Params: map[string]any{
				"index_host": param("string"),
				"vectors":    param("array"),
				"namespace":  paramDefault("string", ""),
			},
		},
		{
			Name:     "pinecone_query_vectors",
			Summary:  "Query a Pinecone index for similar vectors.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host"},
			Params: map[string]any{
				"index_host":       param("string"),
				"vector":           param("array"),
				"id":               paramDefault("string", ""),
				"top_k":            paramDefault("integer", 10),
				"namespace":        paramDefault("string", ""),
				"filter":           param("object"),
				"include_metadata": paramDefault("boolean", true),
				"include_values":   paramDefault("boolean", false),
			},
		},
		{
			Name:     "pinecone_fetch_vectors",
			Summary:  "Fetch vectors by ID from a Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host", "ids"},
			Params: map[string]any{
				"index_host": param("string"),
				"ids":        param("array"),
				"namespace":  paramDefault("string", ""),
			},
		},
		{
			Name:     "pinecone_delete_vectors",
			Summary:  "Delete vectors from a Pinecone index.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host"},
			Params: map[string]any{
				"index_host": param("string"),
				"ids":        param("array"),
				"namespace":  paramDefault("string", ""),
				"delete_all": paramDefault("boolean", false),
				"filter":     param("object"),
			},
		},
		{
			Name:     "pinecone_index_stats",
			Summary:  "Get statistics for a Pinecone index, including namespace vector counts.",
			Source:   "pinecone_tool/pinecone_tool.py",
			Required: []string{"index_host"},
			Params: map[string]any{
				"index_host": param("string"),
			},
		},
	},
}

func pineconeImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"pinecone_list_indexes":   pineconeListIndexes,
		"pinecone_create_index":   pineconeCreateIndex,
		"pinecone_describe_index": pineconeDescribeIndex,
		"pinecone_delete_index":   pineconeDeleteIndex,
		"pinecone_upsert_vectors": pineconeUpsertVectors,
		"pinecone_query_vectors":  pineconeQueryVectors,
		"pinecone_fetch_vectors":  pineconeFetchVectors,
		"pinecone_delete_vectors": pineconeDeleteVectors,
		"pinecone_index_stats":    pineconeIndexStats,
	}
}

// pineconeAuth returns the token or the upstream missing-key error.
func pineconeAuth(rt *Runtime) (string, map[string]any) {
	token := moduleToken(rt, "pinecone_tool")
	if token == "" {
		return "", map[string]any{
			"error": "PINECONE_API_KEY not set",
			"help":  "Get an API key at https://app.pinecone.io/ under API Keys",
		}
	}
	return token, nil
}

// pineconeHeaders mirrors _headers.
func pineconeHeaders(token string) map[string]string {
	return map[string]string{
		"Api-Key":                token,
		"Content-Type":           "application/json",
		"X-Pinecone-Api-Version": pineconeVersion,
	}
}

// pineconeControl performs one control-plane request and maps HTTP
// failures the way _control does. The boolean reports success.
func pineconeControl(
	ctx context.Context,
	rt *Runtime,
	token, method, path string,
	payload any,
) (map[string]any, bool) {
	target := moduleBaseURL(rt, "pinecone_tool")
	if target == "" {
		target = pineconeBaseURL
	}
	reply, err := doRequest(
		ctx, rt, method, target+path, pineconeHeaders(token), payload,
	)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Pinecone timed out"}, false
		}
		return map[string]any{"error": "Pinecone request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your PINECONE_API_KEY."}, false
	}
	if reply.Status == http.StatusAccepted {
		return map[string]any{"status": "accepted"}, true
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Pinecone API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	if len(bytes.TrimSpace(reply.Body)) == 0 {
		return map[string]any{"status": "ok"}, true
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Pinecone request failed: invalid JSON response"}, false
	}
	return value, true
}

// pineconeData performs one data-plane request against the index
// host and maps HTTP failures the way _data does.
func pineconeData(
	ctx context.Context,
	rt *Runtime,
	token, method, host, path string,
	query url.Values,
	payload any,
) (map[string]any, bool) {
	target := host
	if !strings.HasPrefix(target, "https://") && !strings.HasPrefix(target, "http://") {
		target = "https://" + target
	}
	target = strings.TrimRight(target, "/") + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	reply, err := doRequest(
		ctx, rt, method, target, pineconeHeaders(token), payload,
	)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Pinecone timed out"}, false
		}
		return map[string]any{"error": "Pinecone request failed: " + err.Error()}, false
	}
	if reply.Status == http.StatusUnauthorized {
		return map[string]any{"error": "Unauthorized. Check your PINECONE_API_KEY."}, false
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusCreated {
		return map[string]any{
			"error": fmt.Sprintf(
				"Pinecone API error %d: %s",
				reply.Status,
				truncateText(string(reply.Body), maxErrorBytes),
			),
		}, false
	}
	if len(bytes.TrimSpace(reply.Body)) == 0 {
		return map[string]any{}, true
	}
	value, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Pinecone request failed: invalid JSON response"}, false
	}
	return value, true
}

// pineconeStatus reads `(idx.get("status") or {})`.
func pineconeStatus(source map[string]any) map[string]any {
	status, ok := source["status"].(map[string]any)
	if !ok || status == nil {
		return map[string]any{}
	}
	return status
}

func pineconeListIndexes(ctx context.Context, rt *Runtime, _ map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	data, ok := pineconeControl(ctx, rt, token, http.MethodGet, "/indexes", nil)
	if !ok {
		return data, nil
	}
	indexes := make([]map[string]any, 0)
	for _, raw := range asSlice(data["indexes"]) {
		index := asMap(raw)
		status := pineconeStatus(index)
		indexes = append(indexes, map[string]any{
			"name":        asString(index["name"]),
			"dimension":   jsonNumber(intOr(index["dimension"], 0)),
			"metric":      asString(index["metric"]),
			"host":        asString(index["host"]),
			"vector_type": stringDefault(index["vector_type"], "dense"),
			"state":       asString(status["state"]),
			"ready":       boolOr(status["ready"], false),
		})
	}
	return map[string]any{"indexes": indexes, "count": len(indexes)}, nil
}

func pineconeCreateIndex(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	name := stringArg(input, "name")
	dimension := intArg(input, "dimension", 0)
	if name == "" || dimension == 0 {
		return map[string]any{"error": "name and dimension are required"}, nil
	}
	metric := stringDefault(input["metric"], "cosine")
	cloud := stringDefault(input["cloud"], "aws")
	region := stringDefault(input["region"], "us-east-1")
	body := map[string]any{
		"name":      name,
		"dimension": dimension,
		"metric":    metric,
		"spec": map[string]any{
			"serverless": map[string]any{"cloud": cloud, "region": region},
		},
	}
	data, ok := pineconeControl(ctx, rt, token, http.MethodPost, "/indexes", body)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"name":      stringDefault(data["name"], name),
		"dimension": jsonNumber(intOr(data["dimension"], dimension)),
		"metric":    stringDefault(data["metric"], metric),
		"host":      asString(data["host"]),
		"status":    "created",
	}, nil
}

func pineconeDescribeIndex(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexName := stringArg(input, "index_name")
	if indexName == "" {
		return map[string]any{"error": "index_name is required"}, nil
	}
	data, ok := pineconeControl(
		ctx, rt, token, http.MethodGet, "/indexes/"+url.PathEscape(indexName), nil,
	)
	if !ok {
		return data, nil
	}
	status := pineconeStatus(data)
	return map[string]any{
		"name":                asString(data["name"]),
		"dimension":           jsonNumber(intOr(data["dimension"], 0)),
		"metric":              asString(data["metric"]),
		"host":                asString(data["host"]),
		"vector_type":         stringDefault(data["vector_type"], "dense"),
		"state":               asString(status["state"]),
		"ready":               boolOr(status["ready"], false),
		"deletion_protection": stringDefault(data["deletion_protection"], "disabled"),
		"spec":                mapOrEmpty(data["spec"]),
	}, nil
}

func pineconeDeleteIndex(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexName := stringArg(input, "index_name")
	if indexName == "" {
		return map[string]any{"error": "index_name is required"}, nil
	}
	data, ok := pineconeControl(
		ctx, rt, token, http.MethodDelete, "/indexes/"+url.PathEscape(indexName), nil,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{"index_name": indexName, "status": "deleted"}, nil
}

func pineconeUpsertVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	vectors := asSlice(input["vectors"])
	if indexHost == "" || len(vectors) == 0 {
		return map[string]any{"error": "index_host and vectors are required"}, nil
	}
	body := map[string]any{"vectors": vectors}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		body["namespace"] = namespace
	}
	data, ok := pineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/vectors/upsert", nil, body,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"upserted_count": jsonNumber(intOr(data["upsertedCount"], 0)),
	}, nil
}

func pineconeQueryVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	if indexHost == "" {
		return map[string]any{"error": "index_host is required"}, nil
	}
	vector := asSlice(input["vector"])
	id := stringArg(input, "id")
	if len(vector) == 0 && id == "" {
		return map[string]any{"error": "Either vector or id is required"}, nil
	}
	topK := clampInt(intArg(input, "top_k", 10), 1, 10000)
	includeMetadata := boolArg(input, "include_metadata", true)
	includeValues := boolArg(input, "include_values", false)
	body := map[string]any{
		"topK":            topK,
		"includeMetadata": includeMetadata,
		"includeValues":   includeValues,
	}
	if len(vector) > 0 {
		body["vector"] = vector
	}
	if id != "" {
		body["id"] = id
	}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		body["namespace"] = namespace
	}
	if filter, ok := input["filter"].(map[string]any); ok && len(filter) > 0 {
		body["filter"] = filter
	}
	data, ok := pineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/query", nil, body,
	)
	if !ok {
		return data, nil
	}
	matches := make([]map[string]any, 0)
	for _, raw := range asSlice(data["matches"]) {
		source := asMap(raw)
		match := map[string]any{
			"id":    asString(source["id"]),
			"score": floatOr(source["score"], 0),
		}
		if metadata := asMap(source["metadata"]); includeMetadata && len(metadata) > 0 {
			match["metadata"] = metadata
		}
		if values := asSlice(source["values"]); includeValues && len(values) > 0 {
			match["values"] = values
		}
		matches = append(matches, match)
	}
	return map[string]any{
		"matches":   matches,
		"namespace": asString(data["namespace"]),
	}, nil
}

func pineconeFetchVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	ids := toStrings(asSlice(input["ids"]))
	if indexHost == "" || len(ids) == 0 {
		return map[string]any{"error": "index_host and ids are required"}, nil
	}
	query := url.Values{"ids": ids}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		query.Set("namespace", namespace)
	}
	data, ok := pineconeData(
		ctx, rt, token, http.MethodGet, indexHost, "/vectors/fetch", query, nil,
	)
	if !ok {
		return data, nil
	}
	vectors := map[string]any{}
	for id, raw := range asMap(data["vectors"]) {
		source := asMap(raw)
		vectors[id] = map[string]any{
			"id":       stringDefault(source["id"], id),
			"values":   anyList(source["values"]),
			"metadata": source["metadata"],
		}
	}
	return map[string]any{
		"vectors":   vectors,
		"namespace": asString(data["namespace"]),
	}, nil
}

func pineconeDeleteVectors(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	if indexHost == "" {
		return map[string]any{"error": "index_host is required"}, nil
	}
	ids := toStrings(asSlice(input["ids"]))
	deleteAll := boolArg(input, "delete_all", false)
	filter, hasFilter := input["filter"].(map[string]any)
	if len(ids) == 0 && !deleteAll && (!hasFilter || len(filter) == 0) {
		return map[string]any{"error": "Provide ids, delete_all=True, or a filter"}, nil
	}
	body := map[string]any{}
	if len(ids) > 0 {
		body["ids"] = ids
	}
	namespace := stringArg(input, "namespace")
	if namespace != "" {
		body["namespace"] = namespace
	}
	if deleteAll {
		body["deleteAll"] = true
	}
	if hasFilter && len(filter) > 0 {
		body["filter"] = filter
	}
	data, ok := pineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/vectors/delete", nil, body,
	)
	if !ok {
		return data, nil
	}
	return map[string]any{"status": "deleted"}, nil
}

func pineconeIndexStats(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, problem := pineconeAuth(rt)
	if problem != nil {
		return problem, nil
	}
	indexHost := stringArg(input, "index_host")
	if indexHost == "" {
		return map[string]any{"error": "index_host is required"}, nil
	}
	data, ok := pineconeData(
		ctx, rt, token, http.MethodPost, indexHost, "/describe_index_stats", nil,
		map[string]any{},
	)
	if !ok {
		return data, nil
	}
	namespaces := map[string]any{}
	for name, raw := range asMap(data["namespaces"]) {
		namespaces[name] = map[string]any{
			"vector_count": jsonNumber(
				intOr(asMap(raw)["vectorCount"], 0),
			),
		}
	}
	return map[string]any{
		"namespaces":         namespaces,
		"dimension":          jsonNumber(intOr(data["dimension"], 0)),
		"total_vector_count": jsonNumber(intOr(data["totalVectorCount"], 0)),
		"metric":             asString(data["metric"]),
		"vector_type":        asString(data["vectorType"]),
	}, nil
}
