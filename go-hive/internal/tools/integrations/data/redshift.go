// Amazon Redshift Data API (upstream redshift_tool, 5 actions).
//
// The upstream module signs requests with SigV4 in Python; this port performs
// the same signing natively (crypto/hmac, crypto/sha256) and keeps the
// RedshiftData.* action names, payloads and response projections.
//
// Limit: only long-term IAM access keys are supported — no STS session tokens.
package data

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const redshiftService = "redshift-data"

var redshiftTool = &Tool{
	ID:          "redshift_tool",
	Name:        "redshift",
	DisplayName: "Redshift",
	Summary:     "Amazon Redshift Data API integration (5 actions).",
	Actions: []Action{
		{
			Name:     "redshift_execute_sql",
			Summary:  "Execute a SQL statement on Amazon Redshift (async).",
			Source:   "redshift_tool/redshift_tool.py",
			Required: []string{"sql", "database"},
			Params: map[string]any{
				"sql":                param("string"),
				"database":           param("string"),
				"cluster_identifier": paramDefault("string", ""),
				"workgroup_name":     paramDefault("string", ""),
				"secret_arn":         paramDefault("string", ""),
				"db_user":            paramDefault("string", ""),
			},
		},
		{
			Name:     "redshift_describe_statement",
			Summary:  "Check the status of a Redshift SQL statement.",
			Source:   "redshift_tool/redshift_tool.py",
			Required: []string{"statement_id"},
			Params: map[string]any{
				"statement_id": param("string"),
			},
		},
		{
			Name:     "redshift_get_results",
			Summary:  "Fetch results of a completed Redshift SQL statement.",
			Source:   "redshift_tool/redshift_tool.py",
			Required: []string{"statement_id"},
			Params: map[string]any{
				"statement_id": param("string"),
			},
		},
		{
			Name:    "redshift_list_databases",
			Summary: "List databases in a Redshift cluster or workgroup.",
			Source:  "redshift_tool/redshift_tool.py",
			Params: map[string]any{
				"cluster_identifier": paramDefault("string", ""),
				"workgroup_name":     paramDefault("string", ""),
				"database":           paramDefault("string", "dev"),
				"secret_arn":         paramDefault("string", ""),
			},
		},
		{
			Name:     "redshift_list_tables",
			Summary:  "List tables in a Redshift database schema.",
			Source:   "redshift_tool/redshift_tool.py",
			Required: []string{"database"},
			Params: map[string]any{
				"database":           param("string"),
				"schema_pattern":     paramDefault("string", "public"),
				"cluster_identifier": paramDefault("string", ""),
				"workgroup_name":     paramDefault("string", ""),
				"secret_arn":         paramDefault("string", ""),
			},
		},
	},
}

func redshiftImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"redshift_execute_sql":        redshiftExecuteSQL,
		"redshift_describe_statement": redshiftDescribeStatement,
		"redshift_get_results":        redshiftGetResults,
		"redshift_list_databases":     redshiftListDatabases,
		"redshift_list_tables":        redshiftListTables,
	}
}

func redshiftAuthError() map[string]any {
	return map[string]any{
		"error": "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required",
		"help":  "Set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables",
	}
}

func redshiftHMAC(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}

func redshiftSigningKey(secret, datestamp, region string) []byte {
	dateKey := redshiftHMAC([]byte("AWS4"+secret), datestamp)
	regionKey := redshiftHMAC(dateKey, region)
	serviceKey := redshiftHMAC(regionKey, redshiftService)
	return redshiftHMAC(serviceKey, "aws4_request")
}

// redshiftCall signs and posts one Redshift Data API action.
func redshiftCall(ctx context.Context, rt *Runtime, action string, payload map[string]any) map[string]any {
	accessKey := strings.TrimSpace(runtimeEnv(rt, "AWS_ACCESS_KEY_ID"))
	secretKey := strings.TrimSpace(runtimeEnv(rt, "AWS_SECRET_ACCESS_KEY"))
	region := strings.TrimSpace(runtimeEnv(rt, "AWS_REGION"))
	if region == "" {
		region = "us-east-1"
	}
	if accessKey == "" || secretKey == "" {
		return redshiftAuthError()
	}
	base := moduleBaseURL(rt, "redshift_tool")
	if base == "" {
		base = "https://" + redshiftService + "." + region + ".amazonaws.com"
	}
	endpoint := strings.TrimRight(base, "/") + "/"
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return map[string]any{"error": "Redshift request failed: " + err.Error()}
	}
	host := parsed.Host
	if host == "" {
		host = redshiftService + "." + region + ".amazonaws.com"
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return map[string]any{"error": "Redshift request failed: " + err.Error()}
	}
	now := time.Now().UTC()
	if rt != nil && rt.Now != nil {
		now = rt.Now().UTC()
	}
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	payloadHash := sha256.Sum256(body)
	signedHeaders := "content-type;host;x-amz-date;x-amz-target"
	canonicalHeaders := "content-type:application/x-amz-json-1.1\n" +
		"host:" + host + "\n" +
		"x-amz-date:" + amzDate + "\n" +
		"x-amz-target:RedshiftData." + action + "\n"
	canonicalRequest := "POST\n/\n\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + hex.EncodeToString(payloadHash[:])
	credentialScope := datestamp + "/" + region + "/" + redshiftService + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + credentialScope + "\n" +
		hex.EncodeToString(func() []byte {
			sum := sha256.Sum256([]byte(canonicalRequest))
			return sum[:]
		}())
	signature := hex.EncodeToString(redshiftHMAC(
		redshiftSigningKey(secretKey, datestamp, region), stringToSign,
	))
	authorization := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, credentialScope, signedHeaders, signature,
	)
	resp, err := send(ctx, rt, http.MethodPost, endpoint, map[string]string{
		"Content-Type":  "application/x-amz-json-1.1",
		"X-Amz-Date":    amzDate,
		"X-Amz-Target":  "RedshiftData." + action,
		"Authorization": authorization,
	}, body, 30*time.Second)
	if err != nil {
		return transportError(ctx, "Redshift", err)
	}
	if resp.Status >= 400 {
		return httpError(resp)
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Redshift API error: invalid JSON response"}
	}
	return data
}

// redshiftField extracts a Redshift Data API field union value.
func redshiftField(field map[string]any) any {
	if asBool(field["isNull"]) {
		return nil
	}
	for _, key := range []string{"stringValue", "longValue", "doubleValue", "booleanValue", "blobValue"} {
		if value, present := field[key]; present {
			return value
		}
	}
	return nil
}

func redshiftExecuteSQL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sql := stringArg(input, "sql")
	database := stringArg(input, "database")
	cluster := stringArg(input, "cluster_identifier")
	workgroup := stringArg(input, "workgroup_name")
	if strings.TrimSpace(sql) == "" || database == "" {
		return map[string]any{"error": "sql and database are required"}, nil
	}
	if cluster == "" && workgroup == "" {
		return map[string]any{"error": "cluster_identifier or workgroup_name is required"}, nil
	}
	payload := map[string]any{"Sql": sql, "Database": database}
	if cluster != "" {
		payload["ClusterIdentifier"] = cluster
	}
	if workgroup != "" {
		payload["WorkgroupName"] = workgroup
	}
	if secret := stringArg(input, "secret_arn"); secret != "" {
		payload["SecretArn"] = secret
	}
	if dbUser := stringArg(input, "db_user"); dbUser != "" {
		payload["DbUser"] = dbUser
	}
	data := redshiftCall(ctx, rt, "ExecuteStatement", payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	return map[string]any{
		"statement_id": data["Id"],
		"status":       "submitted",
		"database":     data["Database"],
	}, nil
}

func redshiftDescribeStatement(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	statementID := stringArg(input, "statement_id")
	if statementID == "" {
		return map[string]any{"error": "statement_id is required"}, nil
	}
	data := redshiftCall(ctx, rt, "DescribeStatement", map[string]any{"Id": statementID})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	var statementError any
	if text := stringOr(data, "Error", ""); text != "" {
		statementError = text
	}
	return map[string]any{
		"statement_id":   data["Id"],
		"status":         data["Status"],
		"has_result_set": data["HasResultSet"],
		"result_rows":    data["ResultRows"],
		"duration_ns":    data["Duration"],
		"query":          data["QueryString"],
		"error":          statementError,
	}, nil
}

func redshiftGetResults(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	statementID := stringArg(input, "statement_id")
	if statementID == "" {
		return map[string]any{"error": "statement_id is required"}, nil
	}
	data := redshiftCall(ctx, rt, "GetStatementResult", map[string]any{"Id": statementID})
	if _, failed := data["error"]; failed {
		return data, nil
	}
	columns := make([]any, 0)
	for _, rawColumn := range asSlice(data["ColumnMetadata"]) {
		columns = append(columns, asMap(rawColumn)["name"])
	}
	records := asSlice(data["Records"])
	if records == nil {
		records = []any{}
	}
	limit := len(records)
	if limit > 100 {
		limit = 100
	}
	rows := make([]any, 0, limit)
	for _, rawRecord := range records[:limit] {
		fields := asSlice(rawRecord)
		row := make([]any, 0, len(fields))
		for _, rawField := range fields {
			row = append(row, redshiftField(asMap(rawField)))
		}
		rows = append(rows, row)
	}
	return map[string]any{
		"columns":    columns,
		"rows":       rows,
		"total_rows": data["TotalNumRows"],
		"truncated":  len(records) > 100,
	}, nil
}

func redshiftListDatabases(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	cluster := stringArg(input, "cluster_identifier")
	workgroup := stringArg(input, "workgroup_name")
	if cluster == "" && workgroup == "" {
		return map[string]any{"error": "cluster_identifier or workgroup_name is required"}, nil
	}
	payload := map[string]any{
		"Database":   stringOr(input, "database", "dev"),
		"MaxResults": 100,
	}
	if cluster != "" {
		payload["ClusterIdentifier"] = cluster
	}
	if workgroup != "" {
		payload["WorkgroupName"] = workgroup
	}
	if secret := stringArg(input, "secret_arn"); secret != "" {
		payload["SecretArn"] = secret
	}
	data := redshiftCall(ctx, rt, "ListDatabases", payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	databases := asSlice(data["Databases"])
	if databases == nil {
		databases = []any{}
	}
	return map[string]any{"count": len(databases), "databases": databases}, nil
}

func redshiftListTables(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	database := stringArg(input, "database")
	cluster := stringArg(input, "cluster_identifier")
	workgroup := stringArg(input, "workgroup_name")
	if database == "" {
		return map[string]any{"error": "database is required"}, nil
	}
	if cluster == "" && workgroup == "" {
		return map[string]any{"error": "cluster_identifier or workgroup_name is required"}, nil
	}
	payload := map[string]any{
		"Database":      database,
		"SchemaPattern": stringArg(input, "schema_pattern"),
		"MaxResults":    100,
	}
	if cluster != "" {
		payload["ClusterIdentifier"] = cluster
	}
	if workgroup != "" {
		payload["WorkgroupName"] = workgroup
	}
	if secret := stringArg(input, "secret_arn"); secret != "" {
		payload["SecretArn"] = secret
	}
	data := redshiftCall(ctx, rt, "ListTables", payload)
	if _, failed := data["error"]; failed {
		return data, nil
	}
	tables := make([]any, 0)
	for _, rawTable := range asSlice(data["Tables"]) {
		table := asMap(rawTable)
		tables = append(tables, map[string]any{
			"name":   table["name"],
			"schema": table["schema"],
			"type":   table["type"],
		})
	}
	return map[string]any{"count": len(tables), "tables": tables}, nil
}
