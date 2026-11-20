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
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

const batch5MongoMissingMessage = "MONGODB_DATA_API_URL and MONGODB_API_KEY are required"

const batch5MongoHelpText = "Set MONGODB_DATA_API_URL and MONGODB_API_KEY environment variables"

// batch5MongoConfig resolves the endpoint, key and data source.
func batch5MongoConfig(rt *Runtime) (string, string, string, bool) {
	baseURL := batch5Lane2ModuleBaseURL(rt, "mongodb_tool", "MONGODB_DATA_API_URL")
	apiKey := batch5Lane2ConfiguredToken(rt, "mongodb_tool", "MONGODB_API_KEY")
	dataSource := batch5Lane2Getenv(rt, "MONGODB_DATA_SOURCE")
	if baseURL == "" || apiKey == "" {
		return "", "", "", false
	}
	return baseURL, apiKey, dataSource, true
}

// batch5MongoRequest posts one Data API action and returns the decoded body.
func batch5MongoRequest(ctx context.Context, rt *Runtime, action string, body map[string]any) map[string]any {
	baseURL, apiKey, _, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}
	}
	response, err := batch5Lane2Do(ctx, rt, http.MethodPost, baseURL+"/action/"+action, map[string]string{
		"Content-Type": "application/json",
		"api-key":      apiKey,
	}, body)
	if err != nil {
		if batch5Lane2IsTimeout(err) {
			return batch5Lane2Err("MongoDB request timed out")
		}
		return batch5Lane2Err("MongoDB request failed: " + err.Error())
	}
	if response.Status >= 400 {
		return batch5Lane2Err(fmt.Sprintf(
			"HTTP %d: %s",
			response.Status,
			batch5Lane2Truncate(string(response.Body), batch5Lane2MaxError),
		))
	}
	decoded, err := batch5Lane2DecodeJSON(response.Body)
	if err != nil {
		return batch5Lane2Err("MongoDB returned an invalid JSON response")
	}
	data, ok := decoded.(map[string]any)
	if !ok {
		return batch5Lane2Err("MongoDB returned an unexpected response shape")
	}
	return data
}

// batch5MongoEnvelope builds the common Data API body prefix.
func batch5MongoEnvelope(dataSource, database, collection string) map[string]any {
	return map[string]any{
		"dataSource": dataSource,
		"database":   database,
		"collection": collection,
	}
}

// batch5MongoDocuments normalizes a documents field to a JSON array.
func batch5MongoDocuments(value any) []any {
	if documents, ok := value.([]any); ok {
		return documents
	}
	return []any{}
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_find
func batch5Lane2MongoFind(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return batch5Lane2Err("database and collection are required"), nil
	}
	_, _, dataSource, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}, nil
	}
	filter, invalid := batch5Lane2JSONParam(stringArgDefault(input, "filter", "{}"), "filter")
	if invalid != nil {
		return invalid, nil
	}
	body := batch5MongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	body["limit"] = intArg(input, "limit", 20)
	if raw := stringArg(input, "projection"); raw != "" {
		projection, invalid := batch5Lane2JSONParam(raw, "projection")
		if invalid != nil {
			return invalid, nil
		}
		body["projection"] = projection
	}
	if raw := stringArg(input, "sort"); raw != "" {
		sortSpec, invalid := batch5Lane2JSONParam(raw, "sort")
		if invalid != nil {
			return invalid, nil
		}
		body["sort"] = sortSpec
	}
	data := batch5MongoRequest(ctx, rt, "find", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	documents := batch5MongoDocuments(data["documents"])
	return map[string]any{"count": len(documents), "documents": documents}, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_find_one
func batch5Lane2MongoFindOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return batch5Lane2Err("database and collection are required"), nil
	}
	_, _, dataSource, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}, nil
	}
	filter, invalid := batch5Lane2JSONParam(stringArgDefault(input, "filter", "{}"), "filter")
	if invalid != nil {
		return invalid, nil
	}
	body := batch5MongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	if raw := stringArg(input, "projection"); raw != "" {
		projection, invalid := batch5Lane2JSONParam(raw, "projection")
		if invalid != nil {
			return invalid, nil
		}
		body["projection"] = projection
	}
	data := batch5MongoRequest(ctx, rt, "findOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	if data["document"] == nil {
		return batch5Lane2Err("no document found matching filter"), nil
	}
	return data["document"], nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_insert_one
func batch5Lane2MongoInsertOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return batch5Lane2Err("database and collection are required"), nil
	}
	raw := stringArg(input, "document")
	if raw == "" {
		return batch5Lane2Err("document is required"), nil
	}
	document, invalid := batch5Lane2JSONParam(raw, "document")
	if invalid != nil {
		return invalid, nil
	}
	_, _, dataSource, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}, nil
	}
	body := batch5MongoEnvelope(dataSource, database, collection)
	body["document"] = document
	data := batch5MongoRequest(ctx, rt, "insertOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{"result": "inserted", "insertedId": data["insertedId"]}, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_update_one
func batch5Lane2MongoUpdateOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return batch5Lane2Err("database and collection are required"), nil
	}
	rawFilter, rawUpdate := stringArg(input, "filter"), stringArg(input, "update")
	if rawFilter == "" || rawUpdate == "" {
		return batch5Lane2Err("filter and update are required"), nil
	}
	filter, invalid := batch5Lane2JSONParam(rawFilter, "filter")
	if invalid != nil {
		return invalid, nil
	}
	update, invalid := batch5Lane2JSONParam(rawUpdate, "update")
	if invalid != nil {
		return invalid, nil
	}
	_, _, dataSource, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}, nil
	}
	body := batch5MongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	body["update"] = update
	body["upsert"] = boolArg(input, "upsert", false)
	data := batch5MongoRequest(ctx, rt, "updateOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	result := map[string]any{
		"matchedCount":  batch5Lane2IntOrZero(data["matchedCount"]),
		"modifiedCount": batch5Lane2IntOrZero(data["modifiedCount"]),
	}
	if upserted, present := data["upsertedId"]; present {
		result["upsertedId"] = upserted
	}
	return result, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_delete_one
func batch5Lane2MongoDeleteOne(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return batch5Lane2Err("database and collection are required"), nil
	}
	raw := stringArg(input, "filter")
	if raw == "" {
		return batch5Lane2Err("filter is required"), nil
	}
	filter, invalid := batch5Lane2JSONParam(raw, "filter")
	if invalid != nil {
		return invalid, nil
	}
	_, _, dataSource, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}, nil
	}
	body := batch5MongoEnvelope(dataSource, database, collection)
	body["filter"] = filter
	data := batch5MongoRequest(ctx, rt, "deleteOne", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{"deletedCount": batch5Lane2IntOrZero(data["deletedCount"])}, nil
}

// Parity: tools/src/aden_tools/tools/mongodb_tool/mongodb_tool.py::mongodb_aggregate
func batch5Lane2MongoAggregate(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database, collection := stringArg(input, "database"), stringArg(input, "collection")
	if database == "" || collection == "" {
		return batch5Lane2Err("database and collection are required"), nil
	}
	raw := stringArg(input, "pipeline")
	if raw == "" {
		return batch5Lane2Err("pipeline is required"), nil
	}
	pipeline, invalid := batch5Lane2JSONParam(raw, "pipeline")
	if invalid != nil {
		return invalid, nil
	}
	stages, ok := pipeline.([]any)
	if !ok {
		return batch5Lane2Err("pipeline must be a JSON array"), nil
	}
	_, _, dataSource, ok := batch5MongoConfig(rt)
	if !ok {
		return map[string]any{"error": batch5MongoMissingMessage, "help": batch5MongoHelpText}, nil
	}
	body := batch5MongoEnvelope(dataSource, database, collection)
	body["pipeline"] = stages
	data := batch5MongoRequest(ctx, rt, "aggregate", body)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	documents := batch5MongoDocuments(data["documents"])
	return map[string]any{"count": len(documents), "documents": documents}, nil
}

// batch5Lane2IntOrZero converts a JSON number to int, defaulting to 0.
func batch5Lane2IntOrZero(value any) int {
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

// batch5Lane2MongoImpls maps the upstream action names to handlers.
func batch5Lane2MongoImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"mongodb_find":       batch5Lane2MongoFind,
		"mongodb_find_one":   batch5Lane2MongoFindOne,
		"mongodb_insert_one": batch5Lane2MongoInsertOne,
		"mongodb_update_one": batch5Lane2MongoUpdateOne,
		"mongodb_delete_one": batch5Lane2MongoDeleteOne,
		"mongodb_aggregate":  batch5Lane2MongoAggregate,
	}
}

// batch5Lane2MongoTool is the model-facing catalog entry.
var batch5Lane2MongoTool = &Tool{
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
