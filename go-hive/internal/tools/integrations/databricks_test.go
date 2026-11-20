package integrations

import (
	"net/http"
	"strings"
	"testing"
)

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksSqlQuery::test_missing_credentials
func TestBatch6Lane4DatabricksMissingCredentials(t *testing.T) {
	registry := newBatch6Lane4Registry(WithGetenv(func(string) string { return "" }))
	result := batch4Execute(t, registry, "databricks", map[string]any{
		"action": "databricks_sql_query", "statement": "SELECT 1", "warehouse_id": "w1",
	})
	got := batch4ErrorText(t, result)
	if !strings.Contains(got, "DATABRICKS_TOKEN") || !strings.Contains(got, "DATABRICKS_HOST") {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksSqlQuery::test_missing_fields
func TestBatch6Lane4DatabricksSQLQueryMissingFields(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{
		"action": "databricks_sql_query", "statement": "", "warehouse_id": "",
	})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksSqlQuery::test_successful_query
func TestBatch6Lane4DatabricksSQLQuery(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/sql/statements" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"statement_id": "stmt-1",
				"status":       map[string]any{"state": "SUCCEEDED"},
				"manifest": map[string]any{"schema": map[string]any{"columns": []any{
					map[string]any{"name": "id"}, map[string]any{"name": "name"},
				}}},
				"result": map[string]any{"data_array": []any{
					[]any{"1", "Alice"}, []any{"2", "Bob"},
				}},
			})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{
		"action": "databricks_sql_query", "statement": "SELECT * FROM users", "warehouse_id": "w1", "max_rows": 50,
	})
	if result["status"] != "SUCCEEDED" || result["row_count"] != 2 || result["statement_id"] != "stmt-1" {
		t.Fatalf("result = %v", result)
	}
	columns := batch4BodySlice(t, result["columns"])
	if len(columns) != 2 || columns[0] != "id" || columns[1] != "name" {
		t.Fatalf("columns = %v", columns)
	}
	request := recorder.last(t)
	if request.Body["warehouse_id"] != "w1" || request.Body["row_limit"] != float64(50) || request.Body["wait_timeout"] != "30s" {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksListJobs::test_successful_list
func TestBatch6Lane4DatabricksListJobs(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/jobs/list" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"jobs": []any{
				map[string]any{
					"job_id":            1,
					"settings":          map[string]any{"name": "ETL Pipeline"},
					"creator_user_name": "admin@co.com",
					"created_time":      1700000000000,
				},
			}})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{
		"action": "databricks_list_jobs", "max_results": 10, "name_filter": "ETL",
	})
	jobs := batch4BodySlice(t, result["jobs"])
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", jobs)
	}
	job := batch4BodyMap(t, jobs[0])
	if job["name"] != "ETL Pipeline" || job["creator"] != "admin@co.com" {
		t.Fatalf("job = %v", job)
	}
	query := recorder.last(t).Query
	if query.Get("limit") != "10" || query.Get("name") != "ETL" {
		t.Fatalf("query = %v", query)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksRunJob::test_missing_job_id
func TestBatch6Lane4DatabricksRunJobMissingID(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_run_job", "job_id": 0})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksRunJob::test_successful_run
func TestBatch6Lane4DatabricksRunJob(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/jobs/run-now" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"run_id": 42})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_run_job", "job_id": 1})
	if result["run_id"] != float64(42) || result["status"] != "triggered" || result["job_id"] != 1 {
		t.Fatalf("result = %v", result)
	}
	if request := recorder.last(t); request.Body["job_id"] != float64(1) {
		t.Fatalf("body = %v", request.Body)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksGetRun::test_successful_get
func TestBatch6Lane4DatabricksGetRun(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/jobs/runs/get" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"run_id":       42,
				"job_id":       1,
				"state":        map[string]any{"life_cycle_state": "TERMINATED", "result_state": "SUCCESS"},
				"start_time":   1700000000000,
				"run_page_url": "https://test.cloud.databricks.com/run/42",
			})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_get_run", "run_id": 42})
	if result["state"] != "TERMINATED" || result["result_state"] != "SUCCESS" {
		t.Fatalf("result = %v", result)
	}
	if got := recorder.last(t).Query.Get("run_id"); got != "42" {
		t.Fatalf("run_id query = %q", got)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksListClusters::test_successful_list
func TestBatch6Lane4DatabricksListClusters(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/clusters/list" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"clusters": []any{
				map[string]any{
					"cluster_id":        "c-1",
					"cluster_name":      "Dev Cluster",
					"state":             "RUNNING",
					"spark_version":     "14.3.x-scala2.12",
					"creator_user_name": "admin@co.com",
					"num_workers":       4,
				},
			}})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_list_clusters"})
	clusters := batch4BodySlice(t, result["clusters"])
	if len(clusters) != 1 || batch4BodyMap(t, clusters[0])["state"] != "RUNNING" {
		t.Fatalf("clusters = %v", clusters)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksStartCluster::test_missing_cluster_id
func TestBatch6Lane4DatabricksStartClusterMissingID(t *testing.T) {
	registry, _ := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("no request expected")
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_start_cluster", "cluster_id": ""})
	batch4ErrorText(t, result)
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksStartCluster::test_successful_start
func TestBatch6Lane4DatabricksStartCluster(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/clusters/start" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_start_cluster", "cluster_id": "c-1"})
	if result["status"] != "starting" || result["cluster_id"] != "c-1" {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Body["cluster_id"] != "c-1" {
		t.Fatalf("body = %v", recorder.last(t).Body)
	}
}

// Parity-exempt: upstream has no databricks_terminate_cluster test; the
// action is exercised here against the same clusters/delete endpoint as start.
func TestBatch6Lane4DatabricksTerminateCluster(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/clusters/delete" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_terminate_cluster", "cluster_id": "c-1"})
	if result["status"] != "terminating" || result["cluster_id"] != "c-1" {
		t.Fatalf("result = %v", result)
	}
	if recorder.last(t).Body["cluster_id"] != "c-1" {
		t.Fatalf("body = %v", recorder.last(t).Body)
	}
}

// Parity: tools/tests/tools/test_databricks_tool.py::TestDatabricksListWorkspace::test_successful_list
func TestBatch6Lane4DatabricksListWorkspace(t *testing.T) {
	registry, recorder := batch6Lane4TokenRegistry(t, "databricks_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/api/2.0/workspace/list" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"objects": []any{
				map[string]any{"path": "/Users/admin/notebook1", "object_type": "NOTEBOOK", "language": "PYTHON"},
				map[string]any{"path": "/Users/admin/folder1", "object_type": "DIRECTORY", "language": ""},
			}})
		})
	result := batch4Execute(t, registry, "databricks", map[string]any{"action": "databricks_list_workspace"})
	objects := batch4BodySlice(t, result["objects"])
	if len(objects) != 2 || batch4BodyMap(t, objects[0])["object_type"] != "NOTEBOOK" {
		t.Fatalf("objects = %v", objects)
	}
	if result["path"] != "/" || recorder.last(t).Query.Get("path") != "/" {
		t.Fatalf("path = %v / %v", result["path"], recorder.last(t).Query)
	}
}
