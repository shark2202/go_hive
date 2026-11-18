// Databricks workspace, SQL statement execution, jobs and clusters
// (upstream databricks_tool, 8 actions).
//
// Upstream calls /api/2.0/{endpoint} with a personal access token; this port
// preserves the endpoints, bodies, host resolution and error strings.
package integrations

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var batch6Lane4DatabricksTool = &Tool{
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

func batch6Lane4DatabricksImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"databricks_sql_query":         batch6Lane4DatabricksSQLQuery,
		"databricks_list_jobs":         batch6Lane4DatabricksListJobs,
		"databricks_run_job":           batch6Lane4DatabricksRunJob,
		"databricks_get_run":           batch6Lane4DatabricksGetRun,
		"databricks_list_clusters":     batch6Lane4DatabricksListClusters,
		"databricks_start_cluster":     batch6Lane4DatabricksStartCluster,
		"databricks_terminate_cluster": batch6Lane4DatabricksTerminateCluster,
		"databricks_list_workspace":    batch6Lane4DatabricksListWorkspace,
	}
}

// batch6Lane4DatabricksConfig resolves token and host. WithBaseURL wins over
// DATABRICKS_HOST so tests can point at a fake workspace.
func batch6Lane4DatabricksConfig(rt *Runtime) (string, string, bool) {
	token := batch6Lane4ModuleToken(rt, "databricks_tool")
	host := strings.TrimRight(batch6Lane4ModuleURL(rt, "databricks_tool"), "/")
	if host == "" {
		host = strings.TrimRight(strings.TrimSpace(batch6Lane4Getenv(rt, "DATABRICKS_HOST")), "/")
	}
	if token == "" || host == "" {
		return "", "", false
	}
	return token, host, true
}

func batch6Lane4DatabricksAuthError() map[string]any {
	return map[string]any{
		"error": "DATABRICKS_TOKEN or DATABRICKS_HOST not set",
		"help":  "Set DATABRICKS_HOST=https://your-workspace.cloud.databricks.com and DATABRICKS_TOKEN=dapi...",
	}
}

func batch6Lane4DatabricksHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
}

// batch6Lane4DatabricksGet mirrors upstream _get error handling.
func batch6Lane4DatabricksGet(ctx context.Context, rt *Runtime, host, token, endpoint string, params url.Values) map[string]any {
	target := host + "/api/2.0/" + endpoint
	if len(params) > 0 {
		target += "?" + params.Encode()
	}
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodGet, target, batch6Lane4DatabricksHeaders(token), nil, 30*time.Second)
	if err != nil {
		return batch6Lane4DatabricksTransport(ctx, err)
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your DATABRICKS_TOKEN."}
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden: " + batch4TruncateText(string(resp.Body), 300)}
	case http.StatusOK:
		data, ok := batch6Lane4JSONObject(resp.Body)
		if !ok {
			return map[string]any{"error": "Databricks API error: invalid JSON response"}
		}
		return data
	default:
		return map[string]any{
			"error": "Databricks API error " + strconv.Itoa(resp.Status) + ": " + batch4TruncateText(string(resp.Body), 500),
		}
	}
}

// batch6Lane4DatabricksPost mirrors upstream _post error handling.
func batch6Lane4DatabricksPost(ctx context.Context, rt *Runtime, host, token, endpoint string, body map[string]any) map[string]any {
	target := host + "/api/2.0/" + endpoint
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodPost, target, batch6Lane4DatabricksHeaders(token), body, 60*time.Second)
	if err != nil {
		return batch6Lane4DatabricksTransport(ctx, err)
	}
	switch resp.Status {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your DATABRICKS_TOKEN."}
	case http.StatusOK, http.StatusCreated:
		if len(strings.TrimSpace(string(resp.Body))) == 0 {
			return map[string]any{"status": "success"}
		}
		data, ok := batch6Lane4JSONObject(resp.Body)
		if !ok {
			return map[string]any{"error": "Databricks API error: invalid JSON response"}
		}
		return data
	default:
		return map[string]any{
			"error": "Databricks API error " + strconv.Itoa(resp.Status) + ": " + batch4TruncateText(string(resp.Body), 500),
		}
	}
}

func batch6Lane4DatabricksTransport(ctx context.Context, err error) map[string]any {
	if batch4IsTimeout(ctx, err) {
		return map[string]any{"error": "Request to Databricks timed out"}
	}
	return map[string]any{"error": "Databricks request failed: " + err.Error()}
}

func batch6Lane4DatabricksFailure(result map[string]any) (map[string]any, bool) {
	if _, failed := result["error"]; failed {
		return result, true
	}
	return nil, false
}

func batch6Lane4DatabricksSQLQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	statement := stringArg(input, "statement")
	warehouseID := stringArg(input, "warehouse_id")
	if statement == "" || warehouseID == "" {
		return map[string]any{"error": "statement and warehouse_id are required"}, nil
	}
	maxRows := batch6Lane4Clamp(intArg(input, "max_rows", 100), 1, 10000)
	data := batch6Lane4DatabricksPost(ctx, rt, host, token, "sql/statements", map[string]any{
		"statement":    statement,
		"warehouse_id": warehouseID,
		"wait_timeout": "30s",
		"row_limit":    maxRows,
	})
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	statusObject := batch4AsMap(data["status"])
	status := batch6Lane4StringOr(statusObject, "state", "UNKNOWN")
	if status == "FAILED" {
		return map[string]any{
			"error": "SQL query failed: " + batch6Lane4StringOr(batch4AsMap(statusObject["error"]), "message", "Query failed"),
		}, nil
	}
	columns := make([]any, 0)
	for _, rawColumn := range batch4AsSlice(batch4AsMap(batch4AsMap(data["manifest"])["schema"])["columns"]) {
		columns = append(columns, batch6Lane4StringOr(batch4AsMap(rawColumn), "name", ""))
	}
	rows := batch4AsSlice(batch4AsMap(data["result"])["data_array"])
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{
		"status":       status,
		"columns":      columns,
		"rows":         rows,
		"row_count":    len(rows),
		"statement_id": batch6Lane4StringOr(data, "statement_id", ""),
	}, nil
}

func batch6Lane4DatabricksListJobs(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	params := url.Values{}
	params.Set("limit", strconv.Itoa(batch6Lane4Clamp(intArg(input, "max_results", 25), 1, 100)))
	if name := stringArg(input, "name_filter"); name != "" {
		params.Set("name", name)
	}
	data := batch6Lane4DatabricksGet(ctx, rt, host, token, "jobs/list", params)
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	jobs := make([]any, 0)
	for _, rawJob := range batch4AsSlice(data["jobs"]) {
		job := batch4AsMap(rawJob)
		settings := batch4AsMap(job["settings"])
		jobs = append(jobs, map[string]any{
			"job_id":       job["job_id"],
			"name":         batch6Lane4StringOr(settings, "name", ""),
			"creator":      batch6Lane4StringOr(job, "creator_user_name", ""),
			"created_time": job["created_time"],
		})
	}
	return map[string]any{"jobs": jobs}, nil
}

func batch6Lane4DatabricksRunJob(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	jobID := intArg(input, "job_id", 0)
	if jobID == 0 {
		return map[string]any{"error": "job_id is required"}, nil
	}
	data := batch6Lane4DatabricksPost(ctx, rt, host, token, "jobs/run-now", map[string]any{"job_id": jobID})
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	runID := data["run_id"]
	if runID == nil {
		runID = 0
	}
	return map[string]any{"run_id": runID, "job_id": jobID, "status": "triggered"}, nil
}

func batch6Lane4DatabricksGetRun(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	runID := intArg(input, "run_id", 0)
	if runID == 0 {
		return map[string]any{"error": "run_id is required"}, nil
	}
	params := url.Values{"run_id": []string{strconv.Itoa(runID)}}
	data := batch6Lane4DatabricksGet(ctx, rt, host, token, "jobs/runs/get", params)
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	state := batch4AsMap(data["state"])
	return map[string]any{
		"run_id":       data["run_id"],
		"job_id":       data["job_id"],
		"state":        batch6Lane4StringOr(state, "life_cycle_state", ""),
		"result_state": batch6Lane4StringOr(state, "result_state", ""),
		"start_time":   data["start_time"],
		"run_page_url": batch6Lane4StringOr(data, "run_page_url", ""),
	}, nil
}

func batch6Lane4DatabricksListClusters(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	data := batch6Lane4DatabricksGet(ctx, rt, host, token, "clusters/list", nil)
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	clusters := make([]any, 0)
	for _, rawCluster := range batch4AsSlice(data["clusters"]) {
		cluster := batch4AsMap(rawCluster)
		clusters = append(clusters, map[string]any{
			"cluster_id":    batch6Lane4StringOr(cluster, "cluster_id", ""),
			"cluster_name":  batch6Lane4StringOr(cluster, "cluster_name", ""),
			"state":         batch6Lane4StringOr(cluster, "state", ""),
			"spark_version": batch6Lane4StringOr(cluster, "spark_version", ""),
			"creator":       batch6Lane4StringOr(cluster, "creator_user_name", ""),
			"num_workers":   cluster["num_workers"],
		})
	}
	return map[string]any{"clusters": clusters}, nil
}

func batch6Lane4DatabricksStartCluster(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	clusterID := stringArg(input, "cluster_id")
	if clusterID == "" {
		return map[string]any{"error": "cluster_id is required"}, nil
	}
	data := batch6Lane4DatabricksPost(ctx, rt, host, token, "clusters/start", map[string]any{"cluster_id": clusterID})
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	return map[string]any{"status": "starting", "cluster_id": clusterID}, nil
}

func batch6Lane4DatabricksTerminateCluster(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	clusterID := stringArg(input, "cluster_id")
	if clusterID == "" {
		return map[string]any{"error": "cluster_id is required"}, nil
	}
	data := batch6Lane4DatabricksPost(ctx, rt, host, token, "clusters/delete", map[string]any{"cluster_id": clusterID})
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	return map[string]any{"status": "terminating", "cluster_id": clusterID}, nil
}

func batch6Lane4DatabricksListWorkspace(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	token, host, ok := batch6Lane4DatabricksConfig(rt)
	if !ok {
		return batch6Lane4DatabricksAuthError(), nil
	}
	path := stringArg(input, "path")
	if path == "" {
		path = "/"
	}
	params := url.Values{"path": []string{path}}
	data := batch6Lane4DatabricksGet(ctx, rt, host, token, "workspace/list", params)
	if failure, failed := batch6Lane4DatabricksFailure(data); failed {
		return failure, nil
	}
	objects := make([]any, 0)
	for _, rawObject := range batch4AsSlice(data["objects"]) {
		object := batch4AsMap(rawObject)
		objects = append(objects, map[string]any{
			"path":        batch6Lane4StringOr(object, "path", ""),
			"object_type": batch6Lane4StringOr(object, "object_type", ""),
			"language":    batch6Lane4StringOr(object, "language", ""),
		})
	}
	return map[string]any{"path": path, "objects": objects}, nil
}
