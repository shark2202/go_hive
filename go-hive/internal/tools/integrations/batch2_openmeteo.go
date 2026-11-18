package integrations

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// openMeteoCoordinates reads the required latitude/longitude pair.
func openMeteoCoordinates(input map[string]any) (float64, float64, map[string]any) {
	latitude, hasLatitude := batch2Number(input["latitude"])
	longitude, hasLongitude := batch2Number(input["longitude"])
	if !hasLatitude || !hasLongitude {
		return 0, 0, map[string]any{"error": "latitude and longitude are required"}
	}
	return latitude, longitude, nil
}

func openMeteoCoordString(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func openMeteoCurrent(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	latitude, longitude, problem := openMeteoCoordinates(input)
	if problem != nil {
		return problem, nil
	}
	params := url.Values{}
	params.Set("latitude", openMeteoCoordString(latitude))
	params.Set("longitude", openMeteoCoordString(longitude))
	params.Set("current_weather", "true")
	target := batch2BaseURL(rt, "openmeteo_tool", nil) + "/v1/forecast?" + params.Encode()

	status, body, err := batch2Get(ctx, rt, target, nil)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if status < 200 || status >= 300 {
		return map[string]any{"error": fmt.Sprintf("API request failed: %d", status)}, nil
	}
	parsed, decodeProblem := batch2DecodeJSONObject(body)
	if decodeProblem != nil {
		return map[string]any{"error": stringArg(decodeProblem, "error")}, nil
	}
	current, ok := parsed["current_weather"].(map[string]any)
	if !ok {
		return map[string]any{"error": "No current weather data returned"}, nil
	}
	return current, nil
}

func openMeteoForecast(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	latitude, longitude, problem := openMeteoCoordinates(input)
	if problem != nil {
		return problem, nil
	}
	days := intArg(input, "days", 7)
	if days < 1 || days > 16 {
		return map[string]any{"error": "days must be between 1 and 16"}, nil
	}
	params := url.Values{}
	params.Set("latitude", openMeteoCoordString(latitude))
	params.Set("longitude", openMeteoCoordString(longitude))
	params.Set("daily", "temperature_2m_max,temperature_2m_min,precipitation_sum,weathercode")
	params.Set("forecast_days", strconv.Itoa(days))
	params.Set("timezone", "auto")
	target := batch2BaseURL(rt, "openmeteo_tool", nil) + "/v1/forecast?" + params.Encode()

	status, body, err := batch2Get(ctx, rt, target, nil)
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	if status < 200 || status >= 300 {
		return map[string]any{"error": fmt.Sprintf("API request failed: %d", status)}, nil
	}
	parsed, decodeProblem := batch2DecodeJSONObject(body)
	if decodeProblem != nil {
		return map[string]any{"error": stringArg(decodeProblem, "error")}, nil
	}
	daily, _ := parsed["daily"].(map[string]any)
	return map[string]any{
		"dates":           batch2Array(daily, "time"),
		"temperature_max": batch2Array(daily, "temperature_2m_max"),
		"temperature_min": batch2Array(daily, "temperature_2m_min"),
		"precipitation":   batch2Array(daily, "precipitation_sum"),
		"weathercode":     batch2Array(daily, "weathercode"),
	}, nil
}
