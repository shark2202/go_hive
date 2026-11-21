package webintel

// Google Maps module: the upstream split between transport/HTTP failures and
// API-level status values — non-200 responses become "HTTP <status>:
// <body[:200]>", API statuses (REQUEST_DENIED, OVER_QUERY_LIMIT, ...) map onto
// the upstream message table, and OK/ZERO_RESULTS pass through.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ---------------------------------------------------------- Google Maps ---

// googleMapsTool mirrors the upstream module: six Web Service
// actions authenticated by a key query parameter.
var googleMapsTool = &Tool{
	ID:          "google_maps_tool",
	Name:        "google_maps",
	DisplayName: "Google Maps",
	Summary:     "Google Maps integration (6 actions).",
	Actions: []Action{
		{
			Name:    "maps_geocode",
			Summary: "Convert an address to geographic coordinates (latitude/longitude).",
			Source:  "google_maps_tool/google_maps_tool.py",
			Params: map[string]any{
				"address":    paramDefault("string", ""),
				"components": paramDefault("string", ""),
				"bounds":     paramDefault("string", ""),
				"region":     paramDefault("string", ""),
				"language":   paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_reverse_geocode",
			Summary:  "Convert geographic coordinates to a human-readable address.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"latitude", "longitude"},
			Params: map[string]any{
				"latitude":      param("number"),
				"longitude":     param("number"),
				"result_type":   paramDefault("string", ""),
				"location_type": paramDefault("string", ""),
				"language":      paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_directions",
			Summary:  "Calculate routes between two or more locations.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"origin", "destination"},
			Params: map[string]any{
				"origin":         param("string"),
				"destination":    param("string"),
				"mode":           paramDefault("string", "driving"),
				"waypoints":      paramDefault("string", ""),
				"alternatives":   paramDefault("boolean", false),
				"units":          paramDefault("string", "metric"),
				"avoid":          paramDefault("string", ""),
				"departure_time": paramDefault("string", ""),
				"language":       paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_distance_matrix",
			Summary:  "Calculate travel distance and time for multiple origins and destinations.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"origins", "destinations"},
			Params: map[string]any{
				"origins":        param("string"),
				"destinations":   param("string"),
				"mode":           paramDefault("string", "driving"),
				"units":          paramDefault("string", "metric"),
				"avoid":          paramDefault("string", ""),
				"departure_time": paramDefault("string", ""),
				"language":       paramDefault("string", ""),
			},
		},
		{
			Name:     "maps_place_details",
			Summary:  "Get detailed information about a specific place.",
			Source:   "google_maps_tool/google_maps_tool.py",
			Required: []string{"place_id"},
			Params: map[string]any{
				"place_id": param("string"),
				"fields": paramDefault("string",
					"name,formatted_address,geometry,rating,formatted_phone_number,website,opening_hours,reviews,price_level,types"),
				"language":     paramDefault("string", ""),
				"reviews_sort": paramDefault("string", "most_relevant"),
			},
		},
		{
			Name:    "maps_place_search",
			Summary: "Search for places by text query (name, address, or type of place).",
			Source:  "google_maps_tool/google_maps_tool.py",
			Params: map[string]any{
				"query":      paramDefault("string", ""),
				"location":   paramDefault("string", ""),
				"radius":     paramDefault("integer", 0),
				"type":       paramDefault("string", ""),
				"language":   paramDefault("string", ""),
				"opennow":    paramDefault("boolean", false),
				"minprice":   paramDefault("integer", -1),
				"maxprice":   paramDefault("integer", -1),
				"region":     paramDefault("string", ""),
				"page_token": paramDefault("string", ""),
			},
		},
	},
}

// googleMapsImpls binds every action name to its implementation.
func googleMapsImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"maps_geocode":         mapsGeocode,
		"maps_reverse_geocode": mapsReverseGeocode,
		"maps_directions":      mapsDirections,
		"maps_distance_matrix": mapsDistanceMatrix,
		"maps_place_details":   mapsPlaceDetails,
		"maps_place_search":    mapsPlaceSearch,
	}
}

// mapsMissingKey is the upstream missing-credential error.
func mapsMissingKey() map[string]any {
	return map[string]any{
		"error": "Google Maps API key not configured",
		"help": "Set GOOGLE_MAPS_API_KEY environment variable. " +
			"Get a key at https://console.cloud.google.com/apis/credentials " +
			"and enable the Geocoding, Directions, Distance Matrix, and Places APIs.",
	}
}

// mapsStatus checks an API-level status and returns the upstream
// error dict for anything other than OK / ZERO_RESULTS.
func mapsStatus(status, errorMessage string) map[string]any {
	if status == "" {
		status = "UNKNOWN_ERROR"
	}
	if status == "OK" || status == "ZERO_RESULTS" {
		return nil
	}
	messages := map[string]string{
		"OVER_DAILY_LIMIT":        "API key invalid, billing not enabled, or daily limit exceeded",
		"OVER_QUERY_LIMIT":        "Too many requests. Try again later",
		"REQUEST_DENIED":          "Request denied — check that the API is enabled and the key is valid",
		"INVALID_REQUEST":         "Invalid request — check required parameters",
		"MAX_ELEMENTS_EXCEEDED":   "Too many origins × destinations (max 625 elements)",
		"MAX_DIMENSIONS_EXCEEDED": "Too many origins or destinations (max 25 each)",
		"MAX_WAYPOINTS_EXCEEDED":  "Too many waypoints (max 25)",
		"NOT_FOUND":               "One or more locations could not be found",
		"UNKNOWN_ERROR":           "Server error — please retry",
	}
	message, ok := messages[status]
	if !ok {
		message = "API error: " + status
	}
	if errorMessage != "" {
		message = message + ". " + errorMessage
	}
	return map[string]any{"error": message}
}

// mapsGet performs one Maps request and applies the upstream
// HTTP-then-API-status error order. The second return value is the error dict
// when the call failed.
func mapsGet(ctx context.Context, rt *Runtime, key, path string, query url.Values, label string) (map[string]any, map[string]any) {
	query.Set("key", key)
	target := joinQuery(moduleBaseURL(rt, "google_maps_tool")+path, query)
	resp, err := doHTTP(ctx, rt, http.MethodGet, target, nil, nil)
	if err != nil {
		if isTimeout(ctx, err) {
			return nil, map[string]any{"error": "Request timed out"}
		}
		return nil, map[string]any{"error": "Network error: " + err.Error()}
	}
	if resp.Status != http.StatusOK {
		return nil, map[string]any{
			"error": fmt.Sprintf("HTTP %d: %s", resp.Status, truncateText(string(resp.Body), errorTextBytes)),
		}
	}
	data := asMap(resp.Parsed)
	if data == nil {
		return nil, map[string]any{"error": label + " failed: invalid JSON response"}
	}
	status := asString(data["status"])
	errorMessage := asString(data["error_message"])
	if errDict := mapsStatus(status, errorMessage); errDict != nil {
		return nil, errDict
	}
	return data, nil
}

// mapsResults projects the geocoding/reverse-geocoding result
// array.
func mapsResults(data map[string]any) []any {
	results := make([]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		geometry := asMap(item["geometry"])
		location := geometry["location"]
		if location == nil {
			location = map[string]any{}
		}
		results = append(results, map[string]any{
			"formatted_address":  asString(item["formatted_address"]),
			"location":           location,
			"location_type":      asString(geometry["location_type"]),
			"place_id":           asString(item["place_id"]),
			"types":              sliceOrEmpty(item["types"]),
			"address_components": sliceOrEmpty(item["address_components"]),
		})
	}
	return results
}

// mapsGeocode converts an address (or component filter) to
// coordinates.
func mapsGeocode(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	address := stringArg(input, "address")
	components := stringArg(input, "components")
	if address == "" && components == "" {
		return map[string]any{"error": "Either address or components is required"}, nil
	}
	key := moduleToken(rt, "google_maps_tool")
	if key == "" {
		return mapsMissingKey(), nil
	}
	query := url.Values{}
	if address != "" {
		query.Set("address", address)
	}
	for _, name := range []string{"components", "bounds", "region", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	data, errDict := mapsGet(ctx, rt, key, "/geocode/json", query, "Geocoding")
	if errDict != nil {
		return errDict, nil
	}
	results := mapsResults(data)
	queryText := address
	if queryText == "" {
		queryText = components
	}
	return map[string]any{"query": queryText, "results": results, "total": len(results)}, nil
}

// formatCoordinate renders a coordinate like Python's float
// formatting in the upstream f-string: integral values keep a ".0" suffix.
func formatCoordinate(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

// mapsReverseGeocode converts coordinates to an address.
func mapsReverseGeocode(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	latitude, latOK := optionalFloat(input, "latitude")
	if !latOK {
		return map[string]any{"error": "latitude is required"}, nil
	}
	longitude, lngOK := optionalFloat(input, "longitude")
	if !lngOK {
		return map[string]any{"error": "longitude is required"}, nil
	}
	if latitude < -90 || latitude > 90 {
		return map[string]any{"error": "Latitude must be between -90 and 90"}, nil
	}
	if longitude < -180 || longitude > 180 {
		return map[string]any{"error": "Longitude must be between -180 and 180"}, nil
	}
	key := moduleToken(rt, "google_maps_tool")
	if key == "" {
		return mapsMissingKey(), nil
	}
	query := url.Values{}
	query.Set("latlng", formatCoordinate(latitude)+","+formatCoordinate(longitude))
	for _, name := range []string{"result_type", "location_type", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	data, errDict := mapsGet(ctx, rt, key, "/geocode/json", query, "Reverse geocoding")
	if errDict != nil {
		return errDict, nil
	}
	results := mapsResults(data)
	return map[string]any{
		"coordinates": map[string]any{"lat": latitude, "lng": longitude},
		"results":     results,
		"total":       len(results),
	}, nil
}

// mapsDirections calculates routes between locations.
func mapsDirections(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	origin := stringArg(input, "origin")
	if origin == "" {
		return map[string]any{"error": "Origin is required"}, nil
	}
	destination := stringArg(input, "destination")
	if destination == "" {
		return map[string]any{"error": "Destination is required"}, nil
	}
	key := moduleToken(rt, "google_maps_tool")
	if key == "" {
		return mapsMissingKey(), nil
	}
	mode := stringArg(input, "mode")
	query := url.Values{
		"origin":      {origin},
		"destination": {destination},
		"mode":        {mode},
		"units":       {stringArg(input, "units")},
	}
	for _, name := range []string{"waypoints", "avoid", "departure_time", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	if boolArg(input, "alternatives", false) {
		query.Set("alternatives", "true")
	}
	data, errDict := mapsGet(ctx, rt, key, "/directions/json", query, "Directions")
	if errDict != nil {
		return errDict, nil
	}
	routes := make([]any, 0)
	for _, rawRoute := range asSlice(data["routes"]) {
		route := asMap(rawRoute)
		legs := make([]any, 0)
		for _, rawLeg := range asSlice(route["legs"]) {
			leg := asMap(rawLeg)
			steps := make([]any, 0)
			for _, rawStep := range asSlice(leg["steps"]) {
				step := asMap(rawStep)
				steps = append(steps, map[string]any{
					"instruction": asString(step["html_instructions"]),
					"distance":    mapOrEmpty(step["distance"]),
					"duration":    mapOrEmpty(step["duration"]),
					"travel_mode": asString(step["travel_mode"]),
				})
			}
			legs = append(legs, map[string]any{
				"start_address":       asString(leg["start_address"]),
				"end_address":         asString(leg["end_address"]),
				"distance":            mapOrEmpty(leg["distance"]),
				"duration":            mapOrEmpty(leg["duration"]),
				"duration_in_traffic": leg["duration_in_traffic"],
				"steps":               steps,
			})
		}
		routes = append(routes, map[string]any{
			"summary":           asString(route["summary"]),
			"legs":              legs,
			"overview_polyline": asString(asMap(route["overview_polyline"])["points"]),
			"warnings":          sliceOrEmpty(route["warnings"]),
			"waypoint_order":    sliceOrEmpty(route["waypoint_order"]),
		})
	}
	return map[string]any{
		"origin":       origin,
		"destination":  destination,
		"mode":         mode,
		"routes":       routes,
		"total_routes": len(routes),
	}, nil
}

// mapsDistanceMatrix calculates many-to-many travel estimates.
func mapsDistanceMatrix(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	origins := stringArg(input, "origins")
	if origins == "" {
		return map[string]any{"error": "Origins is required"}, nil
	}
	destinations := stringArg(input, "destinations")
	if destinations == "" {
		return map[string]any{"error": "Destinations is required"}, nil
	}
	key := moduleToken(rt, "google_maps_tool")
	if key == "" {
		return mapsMissingKey(), nil
	}
	query := url.Values{
		"origins":      {origins},
		"destinations": {destinations},
		"mode":         {stringArg(input, "mode")},
		"units":        {stringArg(input, "units")},
	}
	for _, name := range []string{"avoid", "departure_time", "language"} {
		if value := stringArg(input, name); value != "" {
			query.Set(name, value)
		}
	}
	data, errDict := mapsGet(ctx, rt, key, "/distancematrix/json", query, "Distance matrix")
	if errDict != nil {
		return errDict, nil
	}
	rows := make([]any, 0)
	for _, rawRow := range asSlice(data["rows"]) {
		row := asMap(rawRow)
		elements := make([]any, 0)
		for _, rawElement := range asSlice(row["elements"]) {
			element := asMap(rawElement)
			projected := map[string]any{
				"status":   asString(element["status"]),
				"distance": mapOrEmpty(element["distance"]),
				"duration": mapOrEmpty(element["duration"]),
			}
			if value, present := element["duration_in_traffic"]; present {
				projected["duration_in_traffic"] = value
			}
			elements = append(elements, projected)
		}
		rows = append(rows, map[string]any{"elements": elements})
	}
	return map[string]any{
		"origin_addresses":      sliceOrEmpty(data["origin_addresses"]),
		"destination_addresses": sliceOrEmpty(data["destination_addresses"]),
		"rows":                  rows,
	}, nil
}

// mapsPlaceDetails reads one place by ID.
func mapsPlaceDetails(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	placeID := stringArg(input, "place_id")
	if placeID == "" {
		return map[string]any{"error": "place_id is required"}, nil
	}
	key := moduleToken(rt, "google_maps_tool")
	if key == "" {
		return mapsMissingKey(), nil
	}
	query := url.Values{
		"place_id":     {placeID},
		"fields":       {stringArg(input, "fields")},
		"reviews_sort": {stringArg(input, "reviews_sort")},
	}
	if language := stringArg(input, "language"); language != "" {
		query.Set("language", language)
	}
	data, errDict := mapsGet(ctx, rt, key, "/place/details/json", query, "Place details")
	if errDict != nil {
		return errDict, nil
	}
	return map[string]any{
		"place_id": placeID,
		"result":   mapOrEmpty(data["result"]),
	}, nil
}

// mapsPlaceSearch runs a text search for places.
func mapsPlaceSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	query := stringArg(input, "query")
	pageToken := stringArg(input, "page_token")
	if query == "" && pageToken == "" {
		return map[string]any{"error": "Query or page_token is required"}, nil
	}
	key := moduleToken(rt, "google_maps_tool")
	if key == "" {
		return mapsMissingKey(), nil
	}
	params := url.Values{}
	if pageToken != "" {
		params.Set("pagetoken", pageToken)
	}
	if query != "" {
		params.Set("query", query)
	}
	for _, name := range []string{"location", "type", "language", "region"} {
		if value := stringArg(input, name); value != "" {
			params.Set(name, value)
		}
	}
	if radius := intArg(input, "radius", 0); radius > 0 {
		params.Set("radius", strconv.Itoa(minInt(radius, 50000)))
	}
	if boolArg(input, "opennow", false) {
		params.Set("opennow", "true")
	}
	if minPrice := intArg(input, "minprice", -1); minPrice >= 0 && minPrice <= 4 {
		params.Set("minprice", strconv.Itoa(minPrice))
	}
	if maxPrice := intArg(input, "maxprice", -1); maxPrice >= 0 && maxPrice <= 4 {
		params.Set("maxprice", strconv.Itoa(maxPrice))
	}
	data, errDict := mapsGet(ctx, rt, key, "/place/textsearch/json", params, "Place search")
	if errDict != nil {
		return errDict, nil
	}
	results := make([]any, 0)
	for _, raw := range asSlice(data["results"]) {
		item := asMap(raw)
		place := map[string]any{
			"name":               asString(item["name"]),
			"formatted_address":  asString(item["formatted_address"]),
			"location":           mapOrEmpty(asMap(item["geometry"])["location"]),
			"place_id":           asString(item["place_id"]),
			"types":              sliceOrEmpty(item["types"]),
			"rating":             item["rating"],
			"user_ratings_total": item["user_ratings_total"],
			"price_level":        item["price_level"],
			"business_status":    asString(item["business_status"]),
		}
		if openingHours, present := item["opening_hours"]; present {
			place["open_now"] = asMap(openingHours)["open_now"]
		}
		results = append(results, place)
	}
	response := map[string]any{"query": query, "results": results, "total": len(results)}
	if token := asString(data["next_page_token"]); token != "" {
		response["next_page_token"] = token
	}
	return response, nil
}
