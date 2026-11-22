package devops

// AWS S3: catalog, SigV4 signing and the eight signed object-storage
// actions.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/tools/integrationkit"
)

// s3Tool publishes the eight S3 object-storage actions.
var s3Tool = &Tool{
	ID:          "aws_s3_tool",
	Name:        "aws_s3",
	DisplayName: "AWS S3",
	Summary:     "AWS S3 buckets and objects over the signed REST API (8 actions).",
	Actions: []Action{
		{
			Name:    "s3_list_buckets",
			Summary: "List all S3 buckets in the account.",
			Source:  "aws_s3_tool/aws_s3_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "s3_list_objects",
			Summary:  "List objects in an S3 bucket.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket"},
			Params: map[string]any{
				"bucket":    integrationkit.Param("string"),
				"prefix":    integrationkit.ParamDefault("string", ""),
				"delimiter": integrationkit.ParamDefault("string", "/"),
				"max_keys":  integrationkit.ParamDefault("integer", 100),
			},
		},
		{
			Name:     "s3_get_object",
			Summary:  "Get an object from S3; returns text content for small objects.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket":    integrationkit.Param("string"),
				"key":       integrationkit.Param("string"),
				"max_bytes": integrationkit.ParamDefault("integer", 10000),
			},
		},
		{
			Name:     "s3_put_object",
			Summary:  "Upload a text object to S3.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key", "content"},
			Params: map[string]any{
				"bucket":       integrationkit.Param("string"),
				"key":          integrationkit.Param("string"),
				"content":      integrationkit.Param("string"),
				"content_type": integrationkit.ParamDefault("string", "text/plain"),
			},
		},
		{
			Name:     "s3_delete_object",
			Summary:  "Delete an object from S3.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket": integrationkit.Param("string"),
				"key":    integrationkit.Param("string"),
			},
		},
		{
			Name:     "s3_copy_object",
			Summary:  "Copy an object within or between S3 buckets.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"source_bucket", "source_key", "dest_bucket", "dest_key"},
			Params: map[string]any{
				"source_bucket": integrationkit.Param("string"),
				"source_key":    integrationkit.Param("string"),
				"dest_bucket":   integrationkit.Param("string"),
				"dest_key":      integrationkit.Param("string"),
			},
		},
		{
			Name:     "s3_get_object_metadata",
			Summary:  "Get object metadata without downloading content (HEAD request).",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket": integrationkit.Param("string"),
				"key":    integrationkit.Param("string"),
			},
		},
		{
			Name:     "s3_generate_presigned_url",
			Summary:  "Generate a pre-signed URL for temporary access to an S3 object.",
			Source:   "aws_s3_tool/aws_s3_tool.py",
			Required: []string{"bucket", "key"},
			Params: map[string]any{
				"bucket":     integrationkit.Param("string"),
				"key":        integrationkit.Param("string"),
				"expires_in": integrationkit.ParamDefault("integer", 3600),
			},
		},
	},
}

func s3Impls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"s3_list_buckets":           s3ListBuckets,
		"s3_list_objects":           s3ListObjects,
		"s3_get_object":             s3GetObject,
		"s3_put_object":             s3PutObject,
		"s3_delete_object":          s3DeleteObject,
		"s3_copy_object":            s3CopyObject,
		"s3_get_object_metadata":    s3GetObjectMetadata,
		"s3_generate_presigned_url": s3GeneratePresignedURL,
	}
}

// ---------------------------------------------------------------- AWS S3 ---

// s3Credentials is the SigV4 credential bundle.
type s3Credentials struct {
	AccessKey string
	SecretKey string
	Region    string
}

// s3Config reads the environment-only AWS credential bundle.
func s3Config(rt *Runtime) (s3Credentials, map[string]any) {
	getenv := runtimeGetenv(rt)
	creds := s3Credentials{
		AccessKey: strings.TrimSpace(getenv("AWS_ACCESS_KEY_ID")),
		SecretKey: strings.TrimSpace(getenv("AWS_SECRET_ACCESS_KEY")),
		Region:    strings.TrimSpace(getenv("AWS_REGION")),
	}
	if creds.Region == "" {
		creds.Region = "us-east-1"
	}
	if creds.AccessKey == "" || creds.SecretKey == "" {
		return s3Credentials{}, map[string]any{
			"error": "AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY are required",
			"help":  "Set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY environment variables",
		}
	}
	return creds, nil
}

// s3HostBase renders the endpoint root for one bucket.
func s3HostBase(rt *Runtime, bucket, region string) string {
	if base := moduleURL(rt, "aws_s3_tool"); base != "" {
		return base
	}
	if bucket == "" {
		return s3DefaultHost
	}
	return "https://" + bucket + ".s3." + region + ".amazonaws.com"
}

// s3Escape percent-encodes per AWS canonical rules (quote(safe=”)).
func s3Escape(value string) string {
	var b strings.Builder
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' || character == '~' {
			b.WriteByte(character)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", character))
	}
	return b.String()
}

// s3CanonicalQuery renders the sorted canonical query string.
func s3CanonicalQuery(query map[string]string) string {
	keys := make([]string, 0, len(query))
	for name := range query {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		parts = append(parts, s3Escape(name)+"="+s3Escape(query[name]))
	}
	return strings.Join(parts, "&")
}

// s3SignKey derives the SigV4 signing key.
func s3SignKey(secretKey, datestamp, region string) []byte {
	sign := func(key []byte, message string) []byte {
		mac := hmac.New(sha256.New, key)
		mac.Write([]byte(message))
		return mac.Sum(nil)
	}
	dateKey := sign([]byte("AWS4"+secretKey), datestamp)
	regionKey := sign(dateKey, region)
	serviceKey := sign(regionKey, "s3")
	return sign(serviceKey, "aws4_request")
}

// s3SignRequest signs one request in place, mirroring
// _sign_request: sorted canonical query, sorted canonical headers, signed
// header list and the AWS4-HMAC-SHA256 Authorization value.
func s3SignRequest(
	now time.Time,
	method, host, path string,
	query, headers map[string]string,
	body []byte,
	creds s3Credentials,
) map[string]string {
	stamp := now.UTC()
	datestamp := stamp.Format("20060102")
	amzDate := stamp.Format("20060102T150405Z")
	payloadHash := sha256.Sum256(body)
	payloadHex := hex.EncodeToString(payloadHash[:])

	if headers == nil {
		headers = map[string]string{}
	}
	headers["host"] = host
	headers["x-amz-date"] = amzDate
	headers["x-amz-content-sha256"] = payloadHex

	canonicalQuery := s3CanonicalQuery(query)

	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(strings.ToLower(name))
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(headers[name]))
		canonicalHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		method,
		path,
		canonicalQuery,
		canonicalHeaders.String(),
		signedHeaders,
		payloadHex,
	}, "\n")

	scope := datestamp + "/" + creds.Region + "/s3/aws4_request"
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(requestHash[:]),
	}, "\n")

	mac := hmac.New(sha256.New, s3SignKey(creds.SecretKey, datestamp, creds.Region))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	headers["Authorization"] = "AWS4-HMAC-SHA256 Credential=" + creds.AccessKey + "/" + scope +
		",SignedHeaders=" + signedHeaders + ",Signature=" + signature
	return headers
}

// s3Call signs and performs one S3 request against the resolved
// endpoint.
func s3Call(
	ctx context.Context,
	rt *Runtime,
	creds s3Credentials,
	method, bucket, key string,
	query map[string]string,
	body []byte,
	extra map[string]string,
) (httpReply, error) {
	path := "/"
	if key != "" {
		path = "/" + key
	}
	base := s3HostBase(rt, bucket, creds.Region)
	target := base + path
	if canonical := s3CanonicalQuery(query); canonical != "" {
		target += "?" + canonical
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return httpReply{}, err
	}
	headers := make(map[string]string, len(extra)+4)
	for name, value := range extra {
		headers[name] = value
	}
	headers = s3SignRequest(runtimeNow(rt), method, parsed.Host, path, query, headers, body, creds)
	return doRequest(ctx, rt, method, target, headers, body)
}

// s3Request resolves credentials and performs one signed call,
// returning the upstream-shaped error dict on credential or transport failure.
func s3Request(
	ctx context.Context,
	rt *Runtime,
	method, bucket, key string,
	query map[string]string,
	body []byte,
	extra map[string]string,
) (httpReply, map[string]any) {
	creds, authErr := s3Config(rt)
	if authErr != nil {
		return httpReply{}, authErr
	}
	reply, err := s3Call(ctx, rt, creds, method, bucket, key, query, body, extra)
	if err != nil {
		if isTimeout(ctx, err) {
			return httpReply{}, map[string]any{"error": "Request to S3 timed out"}
		}
		return httpReply{}, map[string]any{"error": "S3 request failed: " + err.Error()}
	}
	return reply, nil
}

// s3HTTPError renders the upstream `HTTP {status}: {text[:500]}`
// error payload.
func s3HTTPError(reply httpReply) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("HTTP %d: %s", reply.Status, truncateText(string(reply.Body), maxErrorBytes)),
	}
}

// s3Buckets is the ListAllMyBucketsResult envelope.
type s3Buckets struct {
	Buckets []struct {
		Name         string `xml:"Name"`
		CreationDate string `xml:"CreationDate"`
	} `xml:"Buckets>Bucket"`
}

// s3ListResult is the ListBucketResult envelope.
type s3ListResult struct {
	Contents []struct {
		Key          string `xml:"Key"`
		Size         string `xml:"Size"`
		LastModified string `xml:"LastModified"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
	IsTruncated           string `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

func s3ListBuckets(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	reply, failure := s3Request(ctx, rt, http.MethodGet, "", "", nil, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return s3HTTPError(reply), nil
	}
	var parsed s3Buckets
	if err := xml.Unmarshal(reply.Body, &parsed); err != nil {
		return map[string]any{"error": "S3 request failed: invalid XML response"}, nil
	}
	buckets := make([]map[string]any, 0, len(parsed.Buckets))
	for _, bucket := range parsed.Buckets {
		buckets = append(buckets, map[string]any{
			"name":          bucket.Name,
			"creation_date": bucket.CreationDate,
		})
	}
	return map[string]any{"count": len(buckets), "buckets": buckets}, nil
}

func s3ListObjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := integrationkit.StringArg(input, "bucket")
	if bucket == "" {
		return map[string]any{"error": "bucket is required"}, nil
	}
	query := map[string]string{
		"list-type": "2",
		"max-keys":  strconv.Itoa(integrationkit.IntArg(input, "max_keys", 100)),
	}
	if prefix := integrationkit.StringArg(input, "prefix"); prefix != "" {
		query["prefix"] = prefix
	}
	if delimiter := integrationkit.StringArg(input, "delimiter"); delimiter != "" {
		query["delimiter"] = delimiter
	}
	reply, failure := s3Request(ctx, rt, http.MethodGet, bucket, "", query, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return s3HTTPError(reply), nil
	}
	var parsed s3ListResult
	if err := xml.Unmarshal(reply.Body, &parsed); err != nil {
		return map[string]any{"error": "S3 request failed: invalid XML response"}, nil
	}
	objects := make([]map[string]any, 0, len(parsed.Contents))
	for _, object := range parsed.Contents {
		size, _ := strconv.Atoi(strings.TrimSpace(object.Size))
		objects = append(objects, map[string]any{
			"key":           object.Key,
			"size":          size,
			"last_modified": object.LastModified,
		})
	}
	result := map[string]any{"count": len(objects), "objects": objects}
	if len(parsed.CommonPrefixes) > 0 {
		prefixes := make([]string, 0, len(parsed.CommonPrefixes))
		for _, item := range parsed.CommonPrefixes {
			if item.Prefix != "" {
				prefixes = append(prefixes, item.Prefix)
			}
		}
		if len(prefixes) > 0 {
			result["common_prefixes"] = prefixes
		}
	}
	if parsed.IsTruncated == "true" && parsed.NextContinuationToken != "" {
		result["next_continuation_token"] = parsed.NextContinuationToken
	}
	return result, nil
}

func s3GetObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := integrationkit.StringArg(input, "bucket")
	key := integrationkit.StringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	maxBytes := integrationkit.IntArg(input, "max_bytes", 10000)
	extra := map[string]string{}
	if maxBytes > 0 {
		extra["Range"] = fmt.Sprintf("bytes=0-%d", maxBytes-1)
	}
	reply, failure := s3Request(ctx, rt, http.MethodGet, bucket, key, nil, nil, extra)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return s3HTTPError(reply), nil
	}
	contentType := reply.Header.Get("content-type")
	result := map[string]any{
		"key":           key,
		"content_type":  contentType,
		"size":          headerOrNil(reply.Header, "content-length"),
		"last_modified": headerOrNil(reply.Header, "last-modified"),
		"etag":          headerOrNil(reply.Header, "etag"),
	}
	if strings.Contains(contentType, "text") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") {
		result["content"] = string(reply.Body)
	} else {
		result["content_preview"] = fmt.Sprintf("[binary data, %d bytes]", len(reply.Body))
	}
	return result, nil
}

func s3PutObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := integrationkit.StringArg(input, "bucket")
	key := integrationkit.StringArg(input, "key")
	content := integrationkit.StringArg(input, "content")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	if content == "" {
		return map[string]any{"error": "content is required"}, nil
	}
	contentType := integrationkit.StringArg(input, "content_type")
	if contentType == "" {
		contentType = "text/plain"
	}
	body := []byte(content)
	extra := map[string]string{"content-type": contentType}
	reply, failure := s3Request(ctx, rt, http.MethodPut, bucket, key, nil, body, extra)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return s3HTTPError(reply), nil
	}
	return map[string]any{
		"result": "uploaded",
		"key":    key,
		"etag":   headerOrNil(reply.Header, "etag"),
		"size":   len(body),
	}, nil
}

func s3DeleteObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := integrationkit.StringArg(input, "bucket")
	key := integrationkit.StringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	reply, failure := s3Request(ctx, rt, http.MethodDelete, bucket, key, nil, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return s3HTTPError(reply), nil
	}
	return map[string]any{"result": "deleted", "key": key}, nil
}

func s3CopyObject(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	sourceBucket := integrationkit.StringArg(input, "source_bucket")
	sourceKey := integrationkit.StringArg(input, "source_key")
	destBucket := integrationkit.StringArg(input, "dest_bucket")
	destKey := integrationkit.StringArg(input, "dest_key")
	if sourceBucket == "" || sourceKey == "" || destBucket == "" || destKey == "" {
		return map[string]any{
			"error": "source_bucket, source_key, dest_bucket, and dest_key are required",
		}, nil
	}
	extra := map[string]string{"x-amz-copy-source": "/" + sourceBucket + "/" + sourceKey}
	reply, failure := s3Request(ctx, rt, http.MethodPut, destBucket, destKey, nil, nil, extra)
	if failure != nil {
		return failure, nil
	}
	if reply.Status >= 400 {
		return s3HTTPError(reply), nil
	}
	return map[string]any{
		"result":      "copied",
		"source":      sourceBucket + "/" + sourceKey,
		"destination": destBucket + "/" + destKey,
	}, nil
}

func s3GetObjectMetadata(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := integrationkit.StringArg(input, "bucket")
	key := integrationkit.StringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	reply, failure := s3Request(ctx, rt, http.MethodHead, bucket, key, nil, nil, nil)
	if failure != nil {
		return failure, nil
	}
	if reply.Status == http.StatusNotFound {
		return map[string]any{"error": "Object not found"}, nil
	}
	if reply.Status >= 400 {
		return map[string]any{"error": fmt.Sprintf("HTTP %d", reply.Status)}, nil
	}
	storageClass := reply.Header.Get("x-amz-storage-class")
	if storageClass == "" {
		storageClass = "STANDARD"
	}
	metadata := map[string]any{
		"key":            key,
		"content_type":   reply.Header.Get("content-type"),
		"content_length": headerOrNil(reply.Header, "content-length"),
		"last_modified":  headerOrNil(reply.Header, "last-modified"),
		"etag":           headerOrNil(reply.Header, "etag"),
		"storage_class":  storageClass,
	}
	for name, values := range reply.Header {
		lower := strings.ToLower(name)
		if !strings.HasPrefix(lower, "x-amz-meta-") {
			continue
		}
		metadata["meta_"+lower[len("x-amz-meta-"):]] = strings.Join(values, ", ")
	}
	return metadata, nil
}

func s3GeneratePresignedURL(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	bucket := integrationkit.StringArg(input, "bucket")
	key := integrationkit.StringArg(input, "key")
	if bucket == "" || key == "" {
		return map[string]any{"error": "bucket and key are required"}, nil
	}
	creds, authErr := s3Config(rt)
	if authErr != nil {
		return authErr, nil
	}
	expiresIn := clampInt(integrationkit.IntArg(input, "expires_in", 3600), 1, 604800)

	now := runtimeNow(rt).UTC()
	datestamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := datestamp + "/" + creds.Region + "/s3/aws4_request"

	base := s3HostBase(rt, bucket, creds.Region)
	parsed, err := url.Parse(base)
	if err != nil {
		return map[string]any{"error": "S3 request failed: " + err.Error()}, nil
	}
	path := "/" + key
	query := map[string]string{
		"X-Amz-Algorithm":     "AWS4-HMAC-SHA256",
		"X-Amz-Credential":    creds.AccessKey + "/" + scope,
		"X-Amz-Date":          amzDate,
		"X-Amz-Expires":       strconv.Itoa(expiresIn),
		"X-Amz-SignedHeaders": "host",
	}
	canonicalQuery := s3CanonicalQuery(query)
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		path,
		canonicalQuery,
		"host:" + parsed.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	requestHash := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(requestHash[:]),
	}, "\n")
	mac := hmac.New(sha256.New, s3SignKey(creds.SecretKey, datestamp, creds.Region))
	mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))

	return map[string]any{
		"url":        base + path + "?" + canonicalQuery + "&X-Amz-Signature=" + signature,
		"expires_in": expiresIn,
		"key":        key,
		"bucket":     bucket,
	}, nil
}
