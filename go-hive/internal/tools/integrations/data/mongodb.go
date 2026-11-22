// MongoDB integration (upstream mongodb_tool, 6 actions).
//
// The upstream Python module talks to the MongoDB Atlas Data API over HTTP
// (POST {base}/action/{action} with an api-key header), not to the Mongo wire
// protocol, so this port does the same and needs no driver dependency.
//
// Configuration mirrors upstream: MONGODB_DATA_API_URL, MONGODB_API_KEY and an
// optional MONGODB_DATA_SOURCE. Like upstream, the Data API EOL is a deployment
// concern; compatible replacements (Delbridge, RESTHeart) expose the same
// interface.
package data

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const mongoMissingMessage = "MONGODB_DATA_API_URL and MONGODB_API_KEY are required"

const mongoHelpText = "Set MONGODB_DATA_API_URL and MONGODB_API_KEY environment variables"

// mongoConfig resolves the endpoint, key and data source.
func mongoConfig(rt *Runtime) (string, string, string, bool) {
	baseURL := databaseBaseURL(rt, "mongodb_tool", "MONGODB_DATA_API_URL")
	apiKey := configuredToken(rt, "mongodb_tool", "MONGODB_API_KEY")
	dataSource := getenv(rt, "MONGODB_DATA_SOURCE")
	if baseURL == "" || apiKey == "" {
		return "", "", "", false
	}
	return baseURL, apiKey, dataSource, true
}

// mongoRequest posts one Data API action and returns the decoded body.
func mongoRequest(ctx context.Context, rt *Runtime, action string, body map[string]any) map[string]any {
	baseURL, apiKey, _, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}
	}
	response, err := do(ctx, rt, http.MethodPost, baseURL+"/action/"+action, map[string]string{
		"Content-Type": "application/json",
		"api-key":      apiKey,
	}, body)
	if err != nil {
		if isTimeout(ctx, err) {
			return databaseErr("MongoDB request timed out")
		}
		return databaseErr("MongoDB request failed: " + err.Error())
	}
	if response.Status >= 400 {
		return databaseErr(fmt.Sprintf(
			"HTTP %d: %s",
			response.Status,
			truncate(string(response.Body), maxError),
		))
	}
	decoded, err := decodeJSON(response.Body)
	if err != nil {
		return databaseErr("MongoDB returned an invalid JSON response")
	}
	data, ok := decoded.(map[string]any)
	if !ok {
		return databaseErr("MongoDB returned an unexpected response shape")
	}
	return data
}

// mongoEnvelope builds the common Data API body prefix.
func mongoEnvelope(dataSource, database, collection string) map[string]any {
	return map[string]any{
		"dataSource": dataSource,
		"database":   database,
		"collection": collection,
	}
}

// mongoDocuments normalizes a documents field to a JSON array.
func mongoDocuments(value any) []any {
	if documents, ok := value.([]any); ok {
		return documents
	}
	return []any{}
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_find
func mongoFind(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return databaseErr("database and collection are required"), nil
	}
	_, _, dataSource, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}, nil
	}
	filter, invalid := jsonParam(stringArgDefault(input, "filter", "{}"), "filter")
	if invalid != nil {
		return invalid, nil
	}
	body := mongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	body["limit"] = intArg(input, "limit", 20)
	if raw := stringArg(input, "projection"); raw != "" {
		projection, invalid := jsonParam(raw, "projection")
		if invalid != nil {
			return invalid, nil
		}
		body["projection"] = projection
	}
	if raw := stringArg(input, "sort"); raw != "" {
		sortSpec, invalid := jsonParam(raw, "sort")
		if invalid != nil {
			return invalid, nil
		}
		body["sort"] = sortSpec
	}
	data := mongoRequest(ctx, rt, "find", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	documents := mongoDocuments(data["documents"])
	return map[string]any{"count": len(documents), "documents": documents}, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_find_one
func mongoFindOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return databaseErr("database and collection are required"), nil
	}
	_, _, dataSource, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}, nil
	}
	filter, invalid := jsonParam(stringArgDefault(input, "filter", "{}"), "filter")
	if invalid != nil {
		return invalid, nil
	}
	body := mongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	if raw := stringArg(input, "projection"); raw != "" {
		projection, invalid := jsonParam(raw, "projection")
		if invalid != nil {
			return invalid, nil
		}
		body["projection"] = projection
	}
	data := mongoRequest(ctx, rt, "findOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	if data["document"] == nil {
		return databaseErr("no document found matching filter"), nil
	}
	return data["document"], nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_insert_one
func mongoInsertOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return databaseErr("database and collection are required"), nil
	}
	raw := stringArg(input, "document")
	if raw == "" {
		return databaseErr("document is required"), nil
	}
	document, invalid := jsonParam(raw, "document")
	if invalid != nil {
		return invalid, nil
	}
	_, _, dataSource, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}, nil
	}
	body := mongoEnvelope(dataSource, database, collection)
	body["document"] = document
	data := mongoRequest(ctx, rt, "insertOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{"result": "inserted", "insertedId": data["insertedId"]}, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_update_one
func mongoUpdateOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return databaseErr("database and collection are required"), nil
	}
	rawFilter, rawUpdate := stringArg(input, "filter"), stringArg(input, "update")
	if rawFilter == "" || rawUpdate == "" {
		return databaseErr("filter and update are required"), nil
	}
	filter, invalid := jsonParam(rawFilter, "filter")
	if invalid != nil {
		return invalid, nil
	}
	update, invalid := jsonParam(rawUpdate, "update")
	if invalid != nil {
		return invalid, nil
	}
	_, _, dataSource, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}, nil
	}
	body := mongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	body["update"] = update
	body["upsert"] = boolArg(input, "upsert", false)
	data := mongoRequest(ctx, rt, "updateOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	result := map[string]any{
		"matchedCount":  intOrZero(data["matchedCount"]),
		"modifiedCount": intOrZero(data["modifiedCount"]),
	}
	if upserted, present := data["upsertedId"]; present {
		result["upsertedId"] = upserted
	}
	return result, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_delete_one
func mongoDeleteOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return databaseErr("database and collection are required"), nil
	}
	raw := stringArg(input, "filter")
	if raw == "" {
		return databaseErr("filter is required"), nil
	}
	filter, invalid := jsonParam(raw, "filter")
	if invalid != nil {
		return invalid, nil
	}
	_, _, dataSource, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}, nil
	}
	body := mongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	data := mongoRequest(ctx, rt, "deleteOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{"deletedCount": intOrZero(data["deletedCount"])}, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_aggregate
func mongoAggregate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return databaseErr("database and collection are required"), nil
	}
	raw := stringArg(input, "pipeline")
	if raw == "" {
		return databaseErr("pipeline is required"), nil
	}
	pipeline, invalid := jsonParam(raw, "pipeline")
	if invalid != nil {
		return invalid, nil
	}
	stages, ok := pipeline.([]any)
	if !ok {
		return databaseErr("pipeline must be a JSON array"), nil
	}
	_, _, dataSource, ok := mongoConfig(rt)
	if !ok {
		return map[string]any{"error": mongoMissingMessage, "help": mongoHelpText}, nil
	}
	body := mongoEnvelope(dataSource, database, collection)
	body["pipeline"] = stages
	data := mongoRequest(ctx, rt, "aggregate", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	documents := mongoDocuments(data["documents"])
	return map[string]any{"count": len(documents), "documents": documents}, nil
}

// intOrZero converts a JSON number to int, defaulting to 0.
func intOrZero(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case int64:
		return int(typed)
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed)
		}
	}
	return 0
}

// stringArgDefault returns a string parameter or its fallback when absent/empty.
func stringArgDefault(input map[string]any, name, fallback string) string {
	if value := stringArg(input, name); value != "" {
		return value
	}
	return fallback
}

// mongoImpls maps the upstream action names to handlers.
func mongoImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"mongodb_find":       mongoFind,
		"mongodb_find_one":   mongoFindOne,
		"mongodb_insert_one": mongoInsertOne,
		"mongodb_update_one": mongoUpdateOne,
		"mongodb_delete_one": mongoDeleteOne,
		"mongodb_aggregate":  mongoAggregate,
	}
}

// mongoTool is the model-facing catalog entry.
var mongoTool = &Tool{
	ID:          "mongodb_tool",
	Name:        "mongodb",
	DisplayName: "MongoDB",
	Summary:     "MongoDB Atlas Data API integration (6 actions).",
	Actions: []Action{
		{
			Name:     "mongodb_find",
			Summary:  "Find documents matching a JSON filter.",
			Source:   "mongodb_tool/mongodb_tool.py",
			Required: []string{"database", "collection"},
			Params: map[string]any{
				"database":   param("string"),
				"collection": param("string"),
				"filter":     paramDefault("string", "{}"),
				"projection": paramDefault("string", ""),
				"sort":       paramDefault("string", ""),
				"limit":      paramDefault("integer", 20),
			},
			Requires: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"},
		},
		{
			Name:     "mongodb_find_one",
			Summary:  "Find a single document matching a JSON filter.",
			Source:   "mongodb_tool/mongodb_tool.py",
			Required: []string{"database", "collection"},
			Params: map[string]any{
				"database":   param("string"),
				"collection": param("string"),
				"filter":     paramDefault("string", "{}"),
				"projection": paramDefault("string", ""),
			},
			Requires: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"},
		},
		{
			Name:     "mongodb_insert_one",
			Summary:  "Insert a single JSON document.",
			Source:   "mongodb_tool/mongodb_tool.py",
			Required: []string{"database", "collection", "document"},
			Params: map[string]any{
				"database":   param("string"),
				"collection": param("string"),
				"document":   param("string"),
			},
			Requires: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"},
		},
		{
			Name:     "mongodb_update_one",
			Summary:  "Update one document with JSON update operators.",
			Source:   "mongodb_tool/mongodb_tool.py",
			Required: []string{"database", "collection", "filter", "update"},
			Params: map[string]any{
				"database":   param("string"),
				"collection": param("string"),
				"filter":     param("string"),
				"update":     param("string"),
				"upsert":     paramDefault("boolean", false),
			},
			Requires: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"},
		},
		{
			Name:     "mongodb_delete_one",
			Summary:  "Delete one document matching a JSON filter.",
			Source:   "mongodb_tool/mongodb_tool.py",
			Required: []string{"database", "collection", "filter"},
			Params: map[string]any{
				"database":   param("string"),
				"collection": param("string"),
				"filter":     param("string"),
			},
			Requires: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"},
		},
		{
			Name:     "mongodb_aggregate",
			Summary:  "Run a JSON aggregation pipeline.",
			Source:   "mongodb_tool/mongodb_tool.py",
			Required: []string{"database", "collection", "pipeline"},
			Params: map[string]any{
				"database":   param("string"),
				"collection": param("string"),
				"pipeline":   param("string"),
			},
			Requires: []string{"MONGODB_DATA_API_URL", "MONGODB_API_KEY"},
		},
	},
}
