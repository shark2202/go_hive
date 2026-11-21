package infra

// Kafka: catalog, six REST Proxy v3 actions and the topic/consumer-group
// projections.

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
)

var kafkaTool = &Tool{
	ID:          "kafka_tool",
	Name:        "kafka",
	DisplayName: "Apache Kafka (REST Proxy)",
	Summary:     "Kafka topic, produce and consumer-group management via Confluent REST Proxy v3 (6 actions).",
	Actions: []Action{
		{
			Name:    "kafka_list_topics",
			Summary: "List all Kafka topics in the cluster.",
			Source:  "kafka_tool/kafka_tool.py",
		},
		{
			Name:     "kafka_get_topic",
			Summary:  "Get metadata for a specific Kafka topic.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"topic_name"},
			Params: map[string]any{
				"topic_name": param("string"),
			},
		},
		{
			Name:     "kafka_create_topic",
			Summary:  "Create a new Kafka topic.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"topic_name"},
			Params: map[string]any{
				"topic_name":         param("string"),
				"partitions_count":   paramDefault("integer", 1),
				"replication_factor": paramDefault("integer", 3),
			},
		},
		{
			Name:     "kafka_produce_message",
			Summary:  "Produce a message to a Kafka topic.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"topic_name", "value"},
			Params: map[string]any{
				"topic_name": param("string"),
				"value":      param("string"),
				"key":        paramDefault("string", ""),
				"value_type": paramDefault("string", "JSON"),
			},
		},
		{
			Name:    "kafka_list_consumer_groups",
			Summary: "List all consumer groups in the Kafka cluster.",
			Source:  "kafka_tool/kafka_tool.py",
		},
		{
			Name:     "kafka_get_consumer_group_lag",
			Summary:  "Get lag summary for a Kafka consumer group.",
			Source:   "kafka_tool/kafka_tool.py",
			Required: []string{"consumer_group_id"},
			Params: map[string]any{
				"consumer_group_id": param("string"),
			},
		},
	},
}

func kafkaImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"kafka_list_topics":            kafkaListTopics,
		"kafka_get_topic":              kafkaGetTopic,
		"kafka_create_topic":           kafkaCreateTopic,
		"kafka_produce_message":        kafkaProduceMessage,
		"kafka_list_consumer_groups":   kafkaListConsumerGroups,
		"kafka_get_consumer_group_lag": kafkaGetConsumerGroupLag,
	}
}

// kafkaConfig mirrors upstream _get_config. Limit: REST Proxy v3
// endpoints only — no native broker protocol or SASL listener setup.
func kafkaConfig(rt *Runtime) (string, map[string]string, map[string]any) {
	restURL := strings.TrimRight(endpoint(rt, "kafka_tool", "KAFKA_REST_URL", ""), "/")
	if restURL == "" {
		return "", nil, map[string]any{
			"error": "KAFKA_REST_URL is required",
			"help":  "Set KAFKA_REST_URL environment variable",
		}
	}
	clusterID := strings.TrimSpace(runtimeEnv(rt, "KAFKA_CLUSTER_ID"))
	if clusterID == "" {
		return "", nil, map[string]any{
			"error": "KAFKA_CLUSTER_ID is required",
			"help":  "Set KAFKA_CLUSTER_ID environment variable",
		}
	}
	headers := map[string]string{"Content-Type": "application/json"}
	apiKey := runtimeEnv(rt, "KAFKA_API_KEY")
	apiSecret := runtimeEnv(rt, "KAFKA_API_SECRET")
	if apiKey != "" && apiSecret != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(apiKey + ":" + apiSecret))
		headers["Authorization"] = "Basic " + encoded
	}
	return restURL + "/v3/clusters/" + pathEscape(clusterID), headers, nil
}

// kafkaTopic projects one REST Proxy topic resource.
func kafkaTopic(topic map[string]any) map[string]any {
	return map[string]any{
		"name":               topic["topic_name"],
		"partitions_count":   topic["partitions_count"],
		"replication_factor": topic["replication_factor"],
		"is_internal":        topic["is_internal"],
	}
}

func kafkaListTopics(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := kafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data, ok := getJSON(ctx, rt, "Kafka", addPrefix(base, "/topics"), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	topics := listOrEmpty(data["data"])
	out := make([]any, 0, len(topics))
	for _, raw := range topics {
		out = append(out, kafkaTopic(asMap(raw)))
	}
	return map[string]any{"count": len(out), "topics": out}, nil
}

func kafkaGetTopic(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := kafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	topicName := stringArg(input, "topic_name")
	if topicName == "" {
		return map[string]any{"error": "topic_name is required"}, nil
	}
	path := "/topics/" + pathEscape(topicName)
	data, ok := getJSON(ctx, rt, "Kafka", addPrefix(base, path), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	result := kafkaTopic(data)
	result["cluster_id"] = data["cluster_id"]
	return result, nil
}

func kafkaCreateTopic(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := kafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	topicName := stringArg(input, "topic_name")
	if topicName == "" {
		return map[string]any{"error": "topic_name is required"}, nil
	}
	payload := map[string]any{
		"topic_name":         topicName,
		"partitions_count":   intArg(input, "partitions_count", 1),
		"replication_factor": intArg(input, "replication_factor", 3),
	}
	resp, err := doJSON(ctx, rt, http.MethodPost, addPrefix(base, "/topics"), headers, payload, defaultTimeout)
	if err != nil {
		return transportError(ctx, "Kafka", err), nil
	}
	if resp.Status >= 400 {
		return httpError(resp), nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Kafka API error: invalid JSON response"}, nil
	}
	return map[string]any{
		"name":               data["topic_name"],
		"partitions_count":   data["partitions_count"],
		"replication_factor": data["replication_factor"],
	}, nil
}

func kafkaProduceMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := kafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	topicName := stringArg(input, "topic_name")
	value := stringArg(input, "value")
	if topicName == "" || value == "" {
		return map[string]any{"error": "topic_name and value are required"}, nil
	}
	payload := map[string]any{
		"value": map[string]any{"type": stringArg(input, "value_type"), "data": value},
	}
	if key := stringArg(input, "key"); key != "" {
		payload["key"] = map[string]any{"type": "STRING", "data": key}
	}
	path := "/topics/" + pathEscape(topicName) + "/records"
	resp, err := doJSON(ctx, rt, http.MethodPost, addPrefix(base, path), headers, payload, defaultTimeout)
	if err != nil {
		return transportError(ctx, "Kafka", err), nil
	}
	if resp.Status >= 400 {
		return httpError(resp), nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Kafka API error: invalid JSON response"}, nil
	}
	return map[string]any{
		"topic":     data["topic_name"],
		"partition": data["partition_id"],
		"offset":    data["offset"],
		"timestamp": data["timestamp"],
	}, nil
}

func kafkaListConsumerGroups(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := kafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	data, ok := getJSON(ctx, rt, "Kafka", addPrefix(base, "/consumer-groups"), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	groups := listOrEmpty(data["data"])
	out := make([]any, 0, len(groups))
	for _, raw := range groups {
		group := asMap(raw)
		coordinator := asMap(group["coordinator"])
		out = append(out, map[string]any{
			"id":             group["consumer_group_id"],
			"is_simple":      group["is_simple"],
			"state":          group["state"],
			"coordinator_id": coordinator["related"],
		})
	}
	return map[string]any{"count": len(out), "consumer_groups": out}, nil
}

func kafkaGetConsumerGroupLag(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	base, headers, failure := kafkaConfig(rt)
	if failure != nil {
		return failure, nil
	}
	groupID := stringArg(input, "consumer_group_id")
	if groupID == "" {
		return map[string]any{"error": "consumer_group_id is required"}, nil
	}
	path := "/consumer-groups/" + pathEscape(groupID) + "/lag-summary"
	data, ok := getJSON(ctx, rt, "Kafka", addPrefix(base, path), headers, nil, defaultTimeout)
	if !ok {
		return data, nil
	}
	return map[string]any{
		"consumer_group_id":   data["consumer_group_id"],
		"max_lag":             data["max_lag"],
		"max_lag_topic":       data["max_lag_topic_name"],
		"max_lag_partition":   data["max_lag_partition_id"],
		"max_lag_consumer_id": data["max_lag_consumer_id"],
		"total_lag":           data["total_lag"],
	}, nil
}
