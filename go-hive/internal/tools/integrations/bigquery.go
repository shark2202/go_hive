// Google BigQuery REST API (upstream bigquery_tool, 2 actions).
//
// The upstream module is backed by google-cloud-bigquery. This port speaks the
// BigQuery v2 REST API directly so the Go module needs no cloud SDK:
//   - queries:     POST /projects/{project}/queries (+ jobs.getQueryResults)
//   - datasets:    GET  /projects/{project}/datasets/{dataset}/tables[/{table}]
//
// Authentication accepts BIGQUERY_ACCESS_TOKEN (or WithCredential) for an
// already-minted OAuth token, and otherwise mints one from a service-account
// JSON at GOOGLE_APPLICATION_CREDENTIALS via the JWT bearer grant. gcloud ADC
// files are not read — supply BIGQUERY_ACCESS_TOKEN in that setup.
//
// Row typing follows the API's string encoding; INTEGER/FLOAT/BOOLEAN are
// converted back to Go numbers/bools, while NUMERIC/TIMESTAMP remain strings
// (upstream returns Decimal/datetime objects that have no JSON equivalent).
package integrations

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var batch6Lane4BigQueryTool = &Tool{
	ID:          "bigquery_tool",
	Name:        "bigquery",
	DisplayName: "BigQuery",
	Summary:     "Google BigQuery read-only query and dataset exploration (2 actions).",
	Actions: []Action{
		{
			Name:     "run_bigquery_query",
			Summary:  "Execute a read-only SQL query against Google BigQuery.",
			Source:   "bigquery_tool/bigquery_tool.py",
			Required: []string{"sql"},
			Params: map[string]any{
				"sql":        param("string"),
				"project_id": param("string"),
				"max_rows":   paramDefault("integer", 1000),
			},
		},
		{
			Name:     "describe_dataset",
			Summary:  "Describe a BigQuery dataset, listing its tables and their schemas.",
			Source:   "bigquery_tool/bigquery_tool.py",
			Required: []string{"dataset_id"},
			Params: map[string]any{
				"dataset_id": param("string"),
				"project_id": param("string"),
			},
		},
	},
}

func batch6Lane4BigQueryImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"run_bigquery_query": batch6Lane4BigQueryRunQuery,
		"describe_dataset":   batch6Lane4BigQueryDescribeDataset,
	}
}

// batch6Lane4BigQueryWritePattern mirrors upstream WRITE_PATTERN.
var batch6Lane4BigQueryWritePattern = regexp.MustCompile(`(?i)\b(INSERT|UPDATE|DELETE|DROP|CREATE|ALTER|TRUNCATE|MERGE|REPLACE)\b`)

var batch6Lane4BigQueryLineComment = regexp.MustCompile(`(?m)--.*$`)
var batch6Lane4BigQueryBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)

// batch6Lane4BigQueryReadOnly mirrors upstream _is_read_only_query.
func batch6Lane4BigQueryReadOnly(sql string) bool {
	stripped := batch6Lane4BigQueryLineComment.ReplaceAllString(sql, "")
	stripped = batch6Lane4BigQueryBlockComment.ReplaceAllString(stripped, "")
	return !batch6Lane4BigQueryWritePattern.MatchString(stripped)
}

func batch6Lane4BigQueryBase(rt *Runtime) string {
	base := batch6Lane4ModuleURL(rt, "bigquery_tool")
	if base == "" {
		base = "https://bigquery.googleapis.com/bigquery/v2"
	}
	return strings.TrimRight(base, "/")
}

// batch6Lane4BigQueryCredentials is the resolved token/project pair.
type batch6Lane4BigQueryCredentials struct {
	Token   string
	Project string
}

func batch6Lane4BigQueryAuthError(detail string) map[string]any {
	errorText := "BigQuery authentication failed"
	if detail != "" {
		errorText += ": " + detail
	}
	return map[string]any{
		"error": errorText,
		"help": "Set GOOGLE_APPLICATION_CREDENTIALS to your service account JSON path, " +
			"or set BIGQUERY_ACCESS_TOKEN to an OAuth access token. " +
			"(gcloud application-default credential files are not read by this Go port.)",
	}
}

// batch6Lane4BigQueryResolveCredentials resolves an access token and project.
// Precedence: explicit project argument, BIGQUERY_PROJECT_ID, service-account
// project_id.
func batch6Lane4BigQueryResolveCredentials(ctx context.Context, rt *Runtime, requestedProject string) (batch6Lane4BigQueryCredentials, map[string]any) {
	creds := batch6Lane4BigQueryCredentials{
		Token:   batch6Lane4ModuleToken(rt, "bigquery_tool"),
		Project: strings.TrimSpace(requestedProject),
	}
	if creds.Project == "" {
		creds.Project = strings.TrimSpace(batch6Lane4Getenv(rt, "BIGQUERY_PROJECT_ID"))
	}
	credentialsPath := strings.TrimSpace(batch6Lane4Getenv(rt, "GOOGLE_APPLICATION_CREDENTIALS"))
	if creds.Token == "" {
		if credentialsPath == "" {
			return creds, batch6Lane4BigQueryAuthError("no access token or service account file configured")
		}
		token, project, failure := batch6Lane4BigQueryServiceAccountToken(ctx, rt, credentialsPath)
		if failure != nil {
			return creds, failure
		}
		creds.Token = token
		if creds.Project == "" {
			creds.Project = project
		}
	}
	if creds.Project == "" {
		return creds, map[string]any{
			"error": "project_id is required",
			"help":  "Pass project_id or set BIGQUERY_PROJECT_ID (or a service-account project_id).",
		}
	}
	return creds, nil
}

// batch6Lane4BigQueryServiceAccountToken exchanges a service-account JSON key
// for an OAuth access token using the JWT bearer grant.
func batch6Lane4BigQueryServiceAccountToken(ctx context.Context, rt *Runtime, path string) (string, string, map[string]any) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", "", batch6Lane4BigQueryAuthError(err.Error())
	}
	var key struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
		ProjectID   string `json:"project_id"`
	}
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", "", batch6Lane4BigQueryAuthError("invalid service account JSON: " + err.Error())
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return "", "", batch6Lane4BigQueryAuthError("service account JSON is missing client_email or private_key")
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	now := time.Now().UTC()
	if rt != nil && rt.Now != nil {
		now = rt.Now().UTC()
	}
	assertion, err := batch6Lane4BigQueryJWT(key.ClientEmail, key.PrivateKey, tokenURI, now)
	if err != nil {
		return "", "", batch6Lane4BigQueryAuthError(err.Error())
	}
	form := url.Values{
		"grant_type": []string{"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  []string{assertion},
	}
	resp, err := batch6Lane4DoForm(ctx, rt, tokenURI, form, 30*time.Second)
	if err != nil {
		return "", "", batch6Lane4BigQueryAuthError(err.Error())
	}
	if resp.Status >= 400 {
		return "", "", batch6Lane4BigQueryAuthError("token endpoint returned HTTP " + strconv.Itoa(resp.Status))
	}
	data, ok := batch6Lane4JSONObject(resp.Body)
	if !ok {
		return "", "", batch6Lane4BigQueryAuthError("token endpoint returned invalid JSON")
	}
	accessToken := batch6Lane4StringOr(data, "access_token", "")
	if accessToken == "" {
		return "", "", batch6Lane4BigQueryAuthError("token endpoint returned no access_token")
	}
	return accessToken, key.ProjectID, nil
}

// batch6Lane4BigQueryJWT builds an RS256-signed JWT for the Google OAuth
// service-account flow.
func batch6Lane4BigQueryJWT(email, privateKeyPEM, audience string, now time.Time) (string, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return "", fmt.Errorf("service account private_key is not PEM encoded")
	}
	var key *rsa.PrivateKey
	if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return "", fmt.Errorf("service account private_key is not RSA")
		}
		key = rsaKey
	} else if parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		key = parsed
	} else {
		return "", fmt.Errorf("service account private_key could not be parsed")
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims := map[string]any{
		"iss":   email,
		"scope": "https://www.googleapis.com/auth/bigquery",
		"aud":   audience,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Unix(),
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadJSON)
	signingInput := header + "." + payload
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func batch6Lane4BigQueryHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
	}
}

// batch6Lane4BigQueryError maps BigQuery REST errors to the upstream
// user-facing messages.
func batch6Lane4BigQueryError(resp *batch6Lane4Response, datasetID string, describe bool) map[string]any {
	message := batch4TruncateText(string(resp.Body), 500)
	if data, ok := batch6Lane4JSONObject(resp.Body); ok {
		if inner := batch4AsMap(data["error"]); inner != nil {
			if text := batch6Lane4StringOr(inner, "message", ""); text != "" {
				message = text
			}
		}
	}
	lower := strings.ToLower(message)
	switch {
	case resp.Status == http.StatusUnauthorized ||
		strings.Contains(message, "Could not automatically determine credentials") ||
		strings.Contains(lower, "default credentials were not found"):
		return batch6Lane4BigQueryAuthError(message)
	case strings.Contains(message, "Permission") && strings.Contains(lower, "denied"):
		if describe {
			return map[string]any{
				"error": "Permission denied for dataset: " + datasetID,
				"help":  "Ensure your service account has the 'BigQuery Data Viewer' role.",
			}
		}
		return map[string]any{
			"error": "BigQuery permission denied: " + message,
			"help":  "Ensure your service account has the 'BigQuery Data Viewer' and 'BigQuery Job User' roles.",
		}
	case strings.Contains(message, "Not found") || resp.Status == http.StatusNotFound:
		if describe {
			return map[string]any{
				"error": "Dataset not found: " + datasetID,
				"help":  "Check that the dataset exists and you have access to it. Full error: " + message,
			}
		}
		return map[string]any{
			"error": "BigQuery resource not found: " + message,
			"help":  "Check that the project, dataset, and table names are correct.",
		}
	default:
		if describe {
			return map[string]any{"error": "Failed to describe dataset: " + message}
		}
		return map[string]any{"error": "BigQuery query failed: " + message}
	}
}

// batch6Lane4BigQueryScalar converts one REST-encoded value by declared type.
func batch6Lane4BigQueryScalar(value any, fieldType string) any {
	if value == nil {
		return nil
	}
	text, isText := value.(string)
	switch strings.ToUpper(fieldType) {
	case "INTEGER", "INT64", "INT", "SMALLINT", "BIGINT", "TINYINT", "BYTEINT":
		if !isText {
			if parsed, ok := batch6Lane4Int64(value); ok {
				return parsed
			}
			return value
		}
		if parsed, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
			return parsed
		}
		return text
	case "FLOAT", "FLOAT64":
		if !isText {
			if parsed, ok := value.(float64); ok {
				return parsed
			}
			return value
		}
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err == nil {
			return parsed
		}
		return text
	case "BOOLEAN", "BOOL":
		if !isText {
			return batch6Lane4Bool(value)
		}
		switch strings.ToLower(strings.TrimSpace(text)) {
		case "true", "1":
			return true
		default:
			return false
		}
	default:
		return text
	}
}

// batch6Lane4BigQueryValue converts a field value, handling REPEATED and
// RECORD/STRUCT nesting from the REST schema.
func batch6Lane4BigQueryValue(value any, field map[string]any) any {
	if value == nil {
		return nil
	}
	fieldType := strings.ToUpper(batch6Lane4StringOr(field, "type", ""))
	mode := strings.ToUpper(batch6Lane4StringOr(field, "mode", ""))
	if fieldType == "RECORD" || fieldType == "STRUCT" {
		nested := batch4AsSlice(field["fields"])
		if mode == "REPEATED" {
			items := batch4AsSlice(value)
			out := make([]any, 0, len(items))
			for _, item := range items {
				out = append(out, batch6Lane4BigQueryRecord(batch4AsMap(item)["v"], nested))
			}
			return out
		}
		return batch6Lane4BigQueryRecord(value, nested)
	}
	if mode == "REPEATED" {
		items := batch4AsSlice(value)
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, batch6Lane4BigQueryScalar(batch4AsMap(item)["v"], fieldType))
		}
		return out
	}
	return batch6Lane4BigQueryScalar(value, fieldType)
}

// batch6Lane4BigQueryRecord converts one record value ({"f": [...]}).
func batch6Lane4BigQueryRecord(value any, fields []any) map[string]any {
	record := batch4AsMap(value)
	values := batch4AsSlice(record["f"])
	out := make(map[string]any, len(fields))
	for index, rawField := range fields {
		field := batch4AsMap(rawField)
		name := batch6Lane4StringOr(field, "name", "")
		if index >= len(values) {
			out[name] = nil
			continue
		}
		out[name] = batch6Lane4BigQueryValue(batch4AsMap(values[index])["v"], field)
	}
	return out
}

// batch6Lane4BigQueryRows converts REST rows into named dictionaries.
func batch6Lane4BigQueryRows(rows []any, fields []any) []any {
	out := make([]any, 0, len(rows))
	for _, rawRow := range rows {
		values := batch4AsSlice(batch4AsMap(rawRow)["f"])
		row := make(map[string]any, len(fields))
		for index, rawField := range fields {
			field := batch4AsMap(rawField)
			name := batch6Lane4StringOr(field, "name", "")
			if index >= len(values) {
				row[name] = nil
				continue
			}
			row[name] = batch6Lane4BigQueryValue(batch4AsMap(values[index])["v"], field)
		}
		out = append(out, row)
	}
	return out
}

// batch6Lane4BigQuerySchema mirrors upstream _format_schema.
func batch6Lane4BigQuerySchema(fields []any) []any {
	out := make([]any, 0, len(fields))
	for _, rawField := range fields {
		field := batch4AsMap(rawField)
		out = append(out, map[string]any{
			"name": batch6Lane4StringOr(field, "name", ""),
			"type": batch6Lane4StringOr(field, "type", ""),
			"mode": field["mode"],
		})
	}
	return out
}

func batch6Lane4BigQueryRunQuery(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sql := stringArg(input, "sql")
	if !batch6Lane4BigQueryReadOnly(sql) {
		return map[string]any{
			"error": "Write operations are not allowed",
			"help":  "Only SELECT queries are permitted. INSERT, UPDATE, DELETE, DROP, CREATE, ALTER, TRUNCATE, and MERGE are blocked.",
		}, nil
	}
	maxRows := intArg(input, "max_rows", 1000)
	if maxRows < 1 {
		return map[string]any{"error": "max_rows must be at least 1"}, nil
	}
	if maxRows > 10000 {
		return map[string]any{
			"error": "max_rows cannot exceed 10000",
			"help":  "For larger result sets, consider using pagination or exporting to Cloud Storage.",
		}, nil
	}
	creds, failure := batch6Lane4BigQueryResolveCredentials(ctx, rt, stringArg(input, "project_id"))
	if failure != nil {
		return failure, nil
	}
	base := batch6Lane4BigQueryBase(rt)
	endpoint := base + "/projects/" + batch6Lane4PathEscape(creds.Project) + "/queries"
	body := map[string]any{
		"query":        sql,
		"useLegacySql": false,
		"maxResults":   maxRows,
	}
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodPost, endpoint, batch6Lane4BigQueryHeaders(creds.Token), body, 60*time.Second)
	if err != nil {
		return batch6Lane4TransportError(ctx, "BigQuery", err), nil
	}
	if resp.Status >= 400 {
		return batch6Lane4BigQueryError(resp, "", false), nil
	}
	payload, ok := batch6Lane4JSONObject(resp.Body)
	if !ok {
		return map[string]any{"error": "BigQuery API error: invalid JSON response"}, nil
	}
	jobID := batch6Lane4StringOr(batch4AsMap(payload["jobReference"]), "jobId", "")
	// Poll a bounded number of times when the job is still running.
	attempts := 0
	for payload["jobComplete"] == false && jobID != "" && attempts < 10 {
		attempts++
		select {
		case <-ctx.Done():
			return map[string]any{"error": "Request to BigQuery timed out"}, nil
		case <-time.After(time.Second):
		}
		query := url.Values{"maxResults": []string{strconv.Itoa(maxRows)}}
		next, failure := batch6Lane4BigQueryGetResults(ctx, rt, base, creds, jobID, query)
		if failure != nil {
			return failure, nil
		}
		payload = next
	}
	if payload["jobComplete"] == false {
		return map[string]any{"error": "BigQuery query did not complete within 30 seconds"}, nil
	}

	schemaFields := batch4AsSlice(batch4AsMap(payload["schema"])["fields"])
	rows := batch4AsSlice(payload["rows"])
	totalRows, hasTotal := batch6Lane4Int64(payload["totalRows"])
	// Follow page tokens until max_rows or a bounded page count.
	pages := 0
	for pageToken := batch6Lane4StringOr(payload, "pageToken", ""); pageToken != "" && len(rows) < maxRows && pages < 20; {
		pages++
		query := url.Values{
			"maxResults": []string{strconv.Itoa(maxRows - len(rows))},
			"pageToken":  []string{pageToken},
		}
		page, failure := batch6Lane4BigQueryGetResults(ctx, rt, base, creds, jobID, query)
		if failure != nil {
			return failure, nil
		}
		rows = append(rows, batch4AsSlice(page["rows"])...)
		pageToken = batch6Lane4StringOr(page, "pageToken", "")
		if page["schema"] != nil && len(schemaFields) == 0 {
			schemaFields = batch4AsSlice(batch4AsMap(page["schema"])["fields"])
		}
	}
	converted := batch6Lane4BigQueryRows(rows, schemaFields)
	if len(converted) > maxRows {
		converted = converted[:maxRows]
	}
	total := int64(len(converted))
	if hasTotal && totalRows > total {
		total = totalRows
	}
	bytesProcessed, _ := batch6Lane4Int64(payload["totalBytesProcessed"])
	return map[string]any{
		"success":         true,
		"rows":            converted,
		"total_rows":      total,
		"rows_returned":   len(converted),
		"schema":          batch6Lane4BigQuerySchema(schemaFields),
		"bytes_processed": bytesProcessed,
		"query_truncated": total > int64(maxRows),
	}, nil
}

// batch6Lane4BigQueryGetResults fetches one page of query results.
func batch6Lane4BigQueryGetResults(ctx context.Context, rt *Runtime, base string, creds batch6Lane4BigQueryCredentials, jobID string, query url.Values) (map[string]any, map[string]any) {
	endpoint := base + "/projects/" + batch6Lane4PathEscape(creds.Project) +
		"/queries/" + batch6Lane4PathEscape(jobID)
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodGet, endpoint, batch6Lane4BigQueryHeaders(creds.Token), nil, 60*time.Second)
	if err != nil {
		return nil, batch6Lane4TransportError(ctx, "BigQuery", err)
	}
	if resp.Status >= 400 {
		return nil, batch6Lane4BigQueryError(resp, "", false)
	}
	data, ok := batch6Lane4JSONObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "BigQuery API error: invalid JSON response"}
	}
	return data, nil
}

func batch6Lane4BigQueryDescribeDataset(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	datasetID := stringArg(input, "dataset_id")
	if strings.TrimSpace(datasetID) == "" {
		return map[string]any{"error": "dataset_id is required"}, nil
	}
	creds, failure := batch6Lane4BigQueryResolveCredentials(ctx, rt, stringArg(input, "project_id"))
	if failure != nil {
		return failure, nil
	}
	base := batch6Lane4BigQueryBase(rt)
	tableBase := base + "/projects/" + batch6Lane4PathEscape(creds.Project) +
		"/datasets/" + batch6Lane4PathEscape(datasetID) + "/tables"
	tables := make([]any, 0)
	pageToken := ""
	pages := 0
	for {
		pages++
		query := url.Values{"maxResults": []string{"1000"}}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodGet, tableBase+"?"+query.Encode(), batch6Lane4BigQueryHeaders(creds.Token), nil, 60*time.Second)
		if err != nil {
			return batch6Lane4TransportError(ctx, "BigQuery", err), nil
		}
		if resp.Status >= 400 {
			return batch6Lane4BigQueryError(resp, datasetID, true), nil
		}
		page, ok := batch6Lane4JSONObject(resp.Body)
		if !ok {
			return map[string]any{"error": "BigQuery API error: invalid JSON response"}, nil
		}
		tables = append(tables, batch4AsSlice(page["tables"])...)
		pageToken = batch6Lane4StringOr(page, "nextPageToken", "")
		if pageToken == "" || pages >= 20 {
			break
		}
	}

	described := make([]any, 0, len(tables))
	for _, rawTable := range tables {
		table := batch4AsMap(rawTable)
		tableID := batch6Lane4StringOr(batch4AsMap(table["tableReference"]), "tableId", "")
		if tableID == "" {
			continue
		}
		endpoint := tableBase + "/" + batch6Lane4PathEscape(tableID)
		resp, err := batch6Lane4DoJSON(ctx, rt, http.MethodGet, endpoint, batch6Lane4BigQueryHeaders(creds.Token), nil, 60*time.Second)
		if err != nil {
			return batch6Lane4TransportError(ctx, "BigQuery", err), nil
		}
		if resp.Status >= 400 {
			return batch6Lane4BigQueryError(resp, datasetID, true), nil
		}
		detail, ok := batch6Lane4JSONObject(resp.Body)
		if !ok {
			return map[string]any{"error": "BigQuery API error: invalid JSON response"}, nil
		}
		rowCount, _ := batch6Lane4Int64(detail["numRows"])
		sizeBytes, _ := batch6Lane4Int64(detail["numBytes"])
		var rowCountValue any
		if _, present := detail["numRows"]; present {
			rowCountValue = rowCount
		}
		var sizeBytesValue any
		if _, present := detail["numBytes"]; present {
			sizeBytesValue = sizeBytes
		}
		described = append(described, map[string]any{
			"table_id":   batch6Lane4StringOr(batch4AsMap(detail["tableReference"]), "tableId", tableID),
			"type":       batch6Lane4StringOr(detail, "type", ""),
			"row_count":  rowCountValue,
			"size_bytes": sizeBytesValue,
			"columns":    batch6Lane4BigQuerySchema(batch4AsSlice(batch4AsMap(detail["schema"])["fields"])),
		})
	}
	return map[string]any{
		"success":    true,
		"dataset_id": datasetID,
		"project_id": creds.Project,
		"tables":     described,
	}, nil
}
