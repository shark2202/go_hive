// Databricks workspace, SQL statement execution, jobs and clusters
// (upstream databricks_tool, 8 actions).
//
// Upstream calls /api/2.0/{endpoint} with a personal access token; this port
// preserves the endpoints, bodies, host resolution and error strings.
package data

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var databricksTool = &Tool{
	ID:          "databricks_tool",
	Name:        "databricks",
	DisplayName: "Databricks",
	Summary:     "Databricks workspace, SQL, jobs and clusters integration (8 actions).",
	Actions: []Action{
		{
			Name:     "databricks_sql_query",
			Summary:  "Execute a SQL statement on a Databricks SQL Warehouse.",
			Source:   "databricks_tool/databricks_tool.py",
			Required: []string{"statement", "warehouse_id"},
			Params: map[string]any{
				"statement":    param("string"),
				"warehouse_id": param("string"),
				"max_rows":     paramDefault("integer", 100),
			},
		},
		{
			Name:    "databricks_list_jobs",
			Summary: "List jobs in the Databricks workspace.",
			Source:  "databricks_tool/databricks_tool.py",
			Params: map[string]any{
				"max_results": paramDefault("integer", 25),
				"name_filter": paramDefault("string", ""),
			},
		},
		{
			Name:     "databricks_run_job",
			Summary:  "Trigger a job run in Databricks.",
			Source:   "databricks_tool/databricks_tool.py",
			Required: []string{"job_id"},
			Params: map[string]any{
				"job_id": param("integer"),
			},
		},
		{
			Name:     "databricks_get_run",
			Summary:  "Get the status of a Databricks job run.",
			Source:   "databricks_tool/databricks_tool.py",
			Required: []string{"run_id"},
			Params: map[string]any{
				"run_id": param("integer"),
			},
		},
		{
			Name:    "databricks_list_clusters",
			Summary: "List all clusters in the Databricks workspace.",
			Source:  "databricks_tool/databricks_tool.py",
		},
		{
			Name:     "databricks_start_cluster",
			Summary:  "Start a terminated Databricks cluster.",
			Source:   "databricks_tool/databricks_tool.py",
			Required: []string{"cluster_id"},
			Params: map[string]any{
				"cluster_id": param("string"),
			},
		},
		{
			Name:     "databricks_terminate_cluster",
			Summary:  "Terminate a running Databricks cluster.",
			Source:   "databricks_tool/databricks_tool.py",
			Required: []string{"cluster_id"},
			Params: map[string]any{
				"cluster_id": param("string"),
			},
		},
		{
			Name:    "databricks_list_workspace",
			Summary: "List objects in a Databricks workspace directory.",
			Source:  "databricks_tool/databricks_tool.py",
			Params: map[string]any{
				"path": paramDefault("string", "/"),
			},
		},
	},
}

func databricksImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"databricks_sql_query":         databricksSQLQuery,
		"databricks_list_jobs":         databricksListJobs,
		"databricks_run_job":           databricksRunJob,
		"databricks_get_run":           databricksGetRun,
		"databricks_list_clusters":     databricksListClusters,
		"databricks_start_cluster":     databricksStartCluster,
		"databricks_terminate_cluster": databricksTerminateCluster,
		"databricks_list_workspace":    databricksListWorkspace,
	}
}

// databricksConfig resolves token and host. WithBaseURL wins over
// DATABRICKS_HOST so tests can point at a fake workspace.
func databricksConfig(rt *Runtime) (string, string, bool) {
	token := moduleToken(rt, "databricks_tool")
	host := strings.TrimRight(moduleBaseURL(rt, "databricks_tool"), "/")
	if host == "" {
		host = strings.TrimRight(strings.TrimSpace(runtimeEnv(rt, "DATABRICKS_HOST")), "/")
	}
	if token == "" || host == "" {
		return "", "", false
	}
	return token, host, true
}

func databricksAuthError() map[string]any {
	return map[string]any{
		"error": "DATABRICKS_TOKEN or DATABRICKS_HOST not set",
		"help":  "Set DATABRICKS_HOST=https://your-workspace.cloud.databricks.com and DATABRICKS_TOKEN=dapi...",
	}
}

// databricksGet mirrors upstream _get error handling.
func databricksGet(ctx context.Context, rt *Runtime, host, token, endpoint string, params url.Values) map[string]any {
	target := host + "/api/2.0/" + endpoint
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	resp, err := doJSON(ctx, rt, http.MethodGet, target, bearerJSONHeaders(token), nil, 30*time.Second)
	if err != nil {
		return databricksTransport(ctx, err)
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your DATABRICKS_TOKEN."}
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden: " + truncateText(string(resp.Body), 300)}
	case http.StatusOK:
		data, ok := jsonObject(resp.Body)
		if !ok {
			return map[string]any{"error": "Databricks API error: invalid JSON response"}
		}
		return data
	default:
		return map[string]any{
			"error": "Databricks API error " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
}

// databricksPost mirrors upstream _post error handling.
func databricksPost(ctx context.Context, rt *Runtime, host, token, endpoint string, body map[string]any) map[string]any {
	target := host + "/api/2.0/" + endpoint
	resp, err := doJSON(ctx, rt, http.MethodPost, target, bearerJSONHeaders(token), body, 60*time.Second)
	if err != nil {
		return databricksTransport(ctx, err)
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your DATABRICKS_TOKEN."}
	case http.StatusOK, http.StatusCreated:
		if len(strings.TrimSpace(string(resp.Body))) == 0 {
			return map[string]any{"status": "success"}
		}
		data, ok := jsonObject(resp.Body)
		if !ok {
			return map[string]any{"error": "Databricks API error: invalid JSON response"}
		}
		return data
	default:
		return map[string]any{
			"error": "Databricks API error " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
}

func databricksTransport(ctx context.Context, err error) map[string]any {
	if isTimeout(ctx, err) {
		return map[string]any{"error": "Request to Databricks timed out"}
	}
	return map[string]any{"error": "Databricks request failed: " + err.Error()}
}

func databricksSQLQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	statement := stringArg(input, "statement")
	warehouseID := stringArg(input, "warehouse_id")
	if statement == "" || warehouseID == "" {
		return map[string]any{"error": "statement and warehouse_id are required"}, nil
	}
	maxRows := clampInt(intArg(input, "max_rows", 100), 1, 10000)
	data := databricksPost(ctx, rt, host, token, "sql/statements", map[string]any{
		"statement":    statement,
		"warehouse_id": warehouseID,
		"wait_timeout": "30s",
		"row_limit":    maxRows,
	})
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	statusObject := asMap(data["status"])
	status := stringOr(statusObject, "state", "UNKNOWN")
	if status == "FAILED" {
		return map[string]any{
			"error": "SQL query failed: " + stringOr(asMap(statusObject["error"]), "message", "Query failed"),
		}, nil
	}
	columns := make([]any, 0)
	for _, rawColumn := range asSlice(asMap(asMap(data["manifest"])["schema"])["columns"]) {
		columns = append(columns, stringOr(asMap(rawColumn), "name", ""))
	}
	rows := asSlice(asMap(data["result"])["data_array"])
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{
		"status":       status,
		"columns":      columns,
		"rows":         rows,
		"row_count":    len(rows),
		"statement_id": stringOr(data, "statement_id", ""),
	}, nil
}

func databricksListJobs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	params := url.Values{}
	params.Set("limit", strconv.Itoa(clampInt(intArg(input, "max_results", 25), 1, 100)))
	if name := stringArg(input, "name_filter"); name != "" {
		params.Set("name", name)
	}
	data := databricksGet(ctx, rt, host, token, "jobs/list", params)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	jobs := make([]any, 0)
	for _, rawJob := range asSlice(data["jobs"]) {
		job := asMap(rawJob)
		settings := asMap(job["settings"])
		// Upstream reads job.get("job_id", 0) / job.get("created_time", 0): a
		// missing key must render as 0, not JSON null.
		jobID := job["job_id"]
		if jobID == nil {
			jobID = 0
		}
		createdTime := job["created_time"]
		if createdTime == nil {
			createdTime = 0
		}
		jobs = append(jobs, map[string]any{
			"job_id":       jobID,
			"name":         stringOr(settings, "name", ""),
			"creator":      stringOr(job, "creator_user_name", ""),
			"created_time": createdTime,
		})
	}
	return map[string]any{"jobs": jobs}, nil
}

func databricksRunJob(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	jobID := intArg(input, "job_id", 0)
	if jobID == 0 {
		return map[string]any{"error": "job_id is required"}, nil
	}
	data := databricksPost(ctx, rt, host, token, "jobs/run-now", map[string]any{"job_id": jobID})
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	runID := data["run_id"]
	if runID == nil {
		runID = 0
	}
	return map[string]any{"run_id": runID, "job_id": jobID, "status": "triggered"}, nil
}

func databricksGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	runID := intArg(input, "run_id", 0)
	if runID == 0 {
		return map[string]any{"error": "run_id is required"}, nil
	}
	params := url.Values{"run_id": []string{strconv.Itoa(runID)}}
	data := databricksGet(ctx, rt, host, token, "jobs/runs/get", params)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	state := asMap(data["state"])
	// Upstream reads data.get(...) with 0 defaults for run_id/job_id/start_time.
	runIDValue := data["run_id"]
	if runIDValue == nil {
		runIDValue = 0
	}
	jobIDValue := data["job_id"]
	if jobIDValue == nil {
		jobIDValue = 0
	}
	startTime := data["start_time"]
	if startTime == nil {
		startTime = 0
	}
	return map[string]any{
		"run_id":       runIDValue,
		"job_id":       jobIDValue,
		"state":        stringOr(state, "life_cycle_state", ""),
		"result_state": stringOr(state, "result_state", ""),
		"start_time":   startTime,
		"run_page_url": stringOr(data, "run_page_url", ""),
	}, nil
}

func databricksListClusters(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	data := databricksGet(ctx, rt, host, token, "clusters/list", nil)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	clusters := make([]any, 0)
	for _, rawCluster := range asSlice(data["clusters"]) {
		cluster := asMap(rawCluster)
		// Upstream reads c.get("num_workers", 0).
		numWorkers := cluster["num_workers"]
		if numWorkers == nil {
			numWorkers = 0
		}
		clusters = append(clusters, map[string]any{
			"cluster_id":    stringOr(cluster, "cluster_id", ""),
			"cluster_name":  stringOr(cluster, "cluster_name", ""),
			"state":         stringOr(cluster, "state", ""),
			"spark_version": stringOr(cluster, "spark_version", ""),
			"creator":       stringOr(cluster, "creator_user_name", ""),
			"num_workers":   numWorkers,
		})
	}
	return map[string]any{"clusters": clusters}, nil
}

func databricksStartCluster(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	clusterID := stringArg(input, "cluster_id")
	if clusterID == "" {
		return map[string]any{"error": "cluster_id is required"}, nil
	}
	data := databricksPost(ctx, rt, host, token, "clusters/start", map[string]any{"cluster_id": clusterID})
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	return map[string]any{"status": "starting", "cluster_id": clusterID}, nil
}

func databricksTerminateCluster(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	clusterID := stringArg(input, "cluster_id")
	if clusterID == "" {
		return map[string]any{"error": "cluster_id is required"}, nil
	}
	data := databricksPost(ctx, rt, host, token, "clusters/delete", map[string]any{"cluster_id": clusterID})
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	return map[string]any{"status": "terminating", "cluster_id": clusterID}, nil
}

func databricksListWorkspace(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := databricksConfig(rt)
	if !ok {
		return databricksAuthError(), nil
	}
	path := stringArg(input, "path")
	if path == "" {
		path = "/"
	}
	params := url.Values{"path": []string{path}}
	data := databricksGet(ctx, rt, host, token, "workspace/list", params)
	if failure, failed := failureResult(data); failed {
		return failure, nil
	}
	objects := make([]any, 0)
	for _, rawObject := range asSlice(data["objects"]) {
		object := asMap(rawObject)
		objects = append(objects, map[string]any{
			"path":        stringOr(object, "path", ""),
			"object_type": stringOr(object, "object_type", ""),
			"language":    stringOr(object, "language", ""),
		})
	}
	return map[string]any{"path": path, "objects": objects}, nil
}
