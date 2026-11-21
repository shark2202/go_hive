package ai

// Google Cloud Vision image analysis over the REST API.

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// ------------------------------------------------------------------ vision ---

var visionTool = &Tool{
	ID:          "vision_tool",
	Name:        "vision",
	DisplayName: "Google Cloud Vision",
	Summary:     "Google Cloud Vision image analysis over the REST API (9 actions).",
	Actions: []Action{
		{
			Name:     "vision_detect_labels",
			Summary:  "Detect labels (objects, scenes, activities) in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_labels":   paramDefault("integer", 10),
			},
		},
		{
			Name:     "vision_detect_text",
			Summary:  "Extract text from an image (OCR).",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
		{
			Name:     "vision_detect_faces",
			Summary:  "Detect faces and emotions in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_faces":    paramDefault("integer", 10),
			},
		},
		{
			Name:     "vision_localize_objects",
			Summary:  "Detect objects with bounding box coordinates in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_objects":  paramDefault("integer", 10),
			},
		},
		{
			Name:     "vision_detect_logos",
			Summary:  "Detect brand logos in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
				"max_logos":    paramDefault("integer", 5),
			},
		},
		{
			Name:     "vision_detect_landmarks",
			Summary:  "Detect famous landmarks in an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source":  param("string"),
				"max_landmarks": paramDefault("integer", 5),
			},
		},
		{
			Name:     "vision_image_properties",
			Summary:  "Get image properties including dominant colors and crop hints.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
		{
			Name:     "vision_web_detection",
			Summary:  "Find similar images and web references for an image.",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
		{
			Name:     "vision_safe_search",
			Summary:  "Detect inappropriate content in an image (safe search).",
			Source:   "vision_tool/vision_tool.py",
			Required: []string{"image_source"},
			Params: map[string]any{
				"image_source": param("string"),
			},
		},
	},
}

func visionImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"vision_detect_labels":    visionDetectLabels,
		"vision_detect_text":      visionDetectText,
		"vision_detect_faces":     visionDetectFaces,
		"vision_localize_objects": visionLocalizeObjects,
		"vision_detect_logos":     visionDetectLogos,
		"vision_detect_landmarks": visionDetectLandmarks,
		"vision_image_properties": visionImageProperties,
		"vision_web_detection":    visionWebDetection,
		"vision_safe_search":      visionSafeSearch,
	}
}

// visionAuth returns the API key or the upstream auth error.
func visionAuth(rt *Runtime) (string, map[string]any) {
	key := moduleToken(rt, "vision_tool")
	if key == "" {
		return "", map[string]any{
			"error": "GOOGLE_CLOUD_VISION_API_KEY not configured",
			"help":  "Get an API key at https://console.cloud.google.com/apis/credentials",
		}
	}
	return key, nil
}

// visionImage builds the `image` member of one annotate request
// from a public URL or a sandboxed local file.
func visionImage(rt *Runtime, source string) (map[string]any, map[string]any) {
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		return map[string]any{"source": map[string]any{"imageUri": source}}, nil
	}
	if source == "" {
		return nil, map[string]any{"error": "Not a file: " + source}
	}
	path, err := rt.ResolveSafePath(source)
	if err != nil {
		return nil, map[string]any{"error": "File not found: " + source}
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, map[string]any{"error": "File not found: " + source}
	}
	if info.IsDir() {
		return nil, map[string]any{"error": "Not a file: " + source}
	}
	if info.Size() > VisionMaxBytes {
		sizeMB := float64(info.Size()) / (1024 * 1024)
		return nil, map[string]any{
			"error": fmt.Sprintf("File exceeds 10MB limit (%.1fMB)", sizeMB),
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, map[string]any{"error": "Failed to read file: " + err.Error()}
	}
	return map[string]any{
		"content": base64.StdEncoding.EncodeToString(content),
	}, nil
}

// visionFeature builds the single-feature list each detector sends.
func visionFeature(kind string, maxResults int) []any {
	feature := map[string]any{"type": kind}
	if maxResults > 0 {
		feature["maxResults"] = maxResults
	}
	return []any{feature}
}

// visionCall posts one annotate request and maps HTTP failures the
// way _handle_response does. The boolean reports success.
func visionCall(
	ctx context.Context,
	rt *Runtime,
	key string,
	image map[string]any,
	features []any,
) (map[string]any, bool) {
	base := moduleBaseURL(rt, "vision_tool")
	if base == "" {
		base = visionAnnotateURL
	}
	query := url.Values{"key": {key}}
	reply, err := doRequest(
		ctx, rt, http.MethodPost, base+"?"+query.Encode(),
		map[string]string{"Content-Type": "application/json"},
		map[string]any{"requests": []any{map[string]any{"image": image, "features": features}}},
	)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request timed out"}, false
		}
		return map[string]any{"error": "Network error: " + err.Error()}, false
	}
	switch reply.Status {
	case http.StatusBadRequest:
		return map[string]any{
			"error": "Invalid request. Check image format and size.",
		}, false
	case http.StatusUnauthorized:
		return map[string]any{"error": "Invalid API key"}, false
	case http.StatusForbidden:
		return map[string]any{
			"error": "API key not authorized. Enable Vision API in Google Cloud Console.",
		}, false
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limit exceeded. Try again later."}, false
	}
	if reply.Status != http.StatusOK {
		return map[string]any{
			"error": fmt.Sprintf("Vision API error (HTTP %d)", reply.Status),
		}, false
	}
	payload, ok := decodeObject(reply.Body)
	if !ok {
		return map[string]any{"error": "Vision API returned invalid JSON"}, false
	}
	responses := asSlice(payload["responses"])
	if len(responses) == 0 {
		return map[string]any{"error": "Empty response from API"}, false
	}
	result := asMap(responses[0])
	if problem, present := result["error"].(map[string]any); present {
		message := asString(problem["message"])
		if message == "" {
			message = "Unknown API error"
		}
		return map[string]any{"error": message}, false
	}
	return result, true
}

// visionRun loads the image, calls the API and hands the first
// response object to the detector projection.
func visionRun(
	ctx context.Context,
	rt *Runtime,
	input map[string]any,
	features []any,
	project func(map[string]any) any,
) (any, error) {
	key, problem := visionAuth(rt)
	if problem != nil {
		return problem, nil
	}
	image, imageProblem := visionImage(rt, stringArg(input, "image_source"))
	if imageProblem != nil {
		return imageProblem, nil
	}
	result, ok := visionCall(ctx, rt, key, image, features)
	if !ok {
		return result, nil
	}
	return project(result), nil
}

// visionVertices reads `node.poly.vertices` (defaulting to []).
func visionVertices(node map[string]any, poly, vertices string) []any {
	container, _ := node[poly].(map[string]any)
	return anyList(container[vertices])
}

func visionDetectLabels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxLabels := clampInt(intArg(input, "max_labels", 10), 1, 100)
	return visionRun(
		ctx, rt, input,
		visionFeature("LABEL_DETECTION", maxLabels),
		func(result map[string]any) any {
			labels := make([]map[string]any, 0)
			for _, raw := range asSlice(result["labelAnnotations"]) {
				label := asMap(raw)
				labels = append(labels, map[string]any{
					"description": asString(label["description"]),
					"score":       round3(floatOr(label["score"], 0)),
				})
			}
			return map[string]any{"labels": labels}
		},
	)
}

func visionDetectText(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return visionRun(
		ctx, rt, input,
		visionFeature("TEXT_DETECTION", 0),
		func(result map[string]any) any {
			annotations := asSlice(result["textAnnotations"])
			if len(annotations) == 0 {
				return map[string]any{"text": "", "blocks": []any{}}
			}
			blocks := make([]map[string]any, 0, len(annotations)-1)
			for _, raw := range annotations[1:] {
				annotation := asMap(raw)
				blocks = append(blocks, map[string]any{
					"text":   asString(annotation["description"]),
					"bounds": visionVertices(annotation, "boundingPoly", "vertices"),
				})
			}
			return map[string]any{
				"text":   asString(asMap(annotations[0])["description"]),
				"blocks": blocks,
			}
		},
	)
}

func visionDetectFaces(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxFaces := clampInt(intArg(input, "max_faces", 10), 1, 100)
	return visionRun(
		ctx, rt, input,
		visionFeature("FACE_DETECTION", maxFaces),
		func(result map[string]any) any {
			faces := make([]map[string]any, 0)
			for _, raw := range asSlice(result["faceAnnotations"]) {
				face := asMap(raw)
				faces = append(faces, map[string]any{
					"joy":        stringDefault(face["joyLikelihood"], "UNKNOWN"),
					"sorrow":     stringDefault(face["sorrowLikelihood"], "UNKNOWN"),
					"anger":      stringDefault(face["angerLikelihood"], "UNKNOWN"),
					"surprise":   stringDefault(face["surpriseLikelihood"], "UNKNOWN"),
					"confidence": round3(floatOr(face["detectionConfidence"], 0)),
					"bounds":     visionVertices(face, "boundingPoly", "vertices"),
				})
			}
			return map[string]any{"faces": faces}
		},
	)
}

func visionLocalizeObjects(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxObjects := clampInt(intArg(input, "max_objects", 10), 1, 100)
	return visionRun(
		ctx, rt, input,
		visionFeature("OBJECT_LOCALIZATION", maxObjects),
		func(result map[string]any) any {
			objects := make([]map[string]any, 0)
			for _, raw := range asSlice(result["localizedObjectAnnotations"]) {
				object := asMap(raw)
				objects = append(objects, map[string]any{
					"name":   asString(object["name"]),
					"score":  round3(floatOr(object["score"], 0)),
					"bounds": visionVertices(object, "boundingPoly", "normalizedVertices"),
				})
			}
			return map[string]any{"objects": objects}
		},
	)
}

func visionDetectLogos(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxLogos := clampInt(intArg(input, "max_logos", 5), 1, 20)
	return visionRun(
		ctx, rt, input,
		visionFeature("LOGO_DETECTION", maxLogos),
		func(result map[string]any) any {
			logos := make([]map[string]any, 0)
			for _, raw := range asSlice(result["logoAnnotations"]) {
				logo := asMap(raw)
				logos = append(logos, map[string]any{
					"description": asString(logo["description"]),
					"score":       round3(floatOr(logo["score"], 0)),
				})
			}
			return map[string]any{"logos": logos}
		},
	)
}

func visionDetectLandmarks(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	maxLandmarks := clampInt(intArg(input, "max_landmarks", 5), 1, 20)
	return visionRun(
		ctx, rt, input,
		visionFeature("LANDMARK_DETECTION", maxLandmarks),
		func(result map[string]any) any {
			landmarks := make([]map[string]any, 0)
			for _, raw := range asSlice(result["landmarkAnnotations"]) {
				landmark := asMap(raw)
				location := map[string]any{}
				if locations := asSlice(landmark["locations"]); len(locations) > 0 {
					latLng := asMap(asMap(locations[0])["latLng"])
					location = map[string]any{
						"latitude":  latLng["latitude"],
						"longitude": latLng["longitude"],
					}
				}
				landmarks = append(landmarks, map[string]any{
					"description": asString(landmark["description"]),
					"score":       round3(floatOr(landmark["score"], 0)),
					"location":    location,
				})
			}
			return map[string]any{"landmarks": landmarks}
		},
	)
}

func visionImageProperties(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return visionRun(
		ctx, rt, input,
		[]any{
			map[string]any{"type": "IMAGE_PROPERTIES"},
			map[string]any{"type": "CROP_HINTS"},
		},
		func(result map[string]any) any {
			colors := make([]map[string]any, 0)
			dominant := asMap(
				asMap(result["imagePropertiesAnnotation"])["dominantColors"],
			)
			for index, raw := range asSlice(dominant["colors"]) {
				if index >= 5 {
					break
				}
				color := asMap(raw)
				rgb := asMap(color["color"])
				colors = append(colors, map[string]any{
					"red":            int(floatOr(rgb["red"], 0)),
					"green":          int(floatOr(rgb["green"], 0)),
					"blue":           int(floatOr(rgb["blue"], 0)),
					"score":          round3(floatOr(color["score"], 0)),
					"pixel_fraction": round3(floatOr(color["pixelFraction"], 0)),
				})
			}
			cropHints := make([]map[string]any, 0)
			hints := asMap(result["cropHintsAnnotation"])
			for _, raw := range asSlice(hints["cropHints"]) {
				hint := asMap(raw)
				cropHints = append(cropHints, map[string]any{
					"bounds":     visionVertices(hint, "boundingPoly", "vertices"),
					"confidence": round3(floatOr(hint["confidence"], 0)),
				})
			}
			return map[string]any{"colors": colors, "crop_hints": cropHints}
		},
	)
}

func visionWebDetection(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return visionRun(
		ctx, rt, input,
		visionFeature("WEB_DETECTION", 0),
		func(result map[string]any) any {
			web := asMap(result["webDetection"])
			webEntities := make([]map[string]any, 0)
			for index, raw := range asSlice(web["webEntities"]) {
				if index >= 10 {
					break
				}
				entity := asMap(raw)
				webEntities = append(webEntities, map[string]any{
					"description": asString(entity["description"]),
					"score":       round3(floatOr(entity["score"], 0)),
				})
			}
			similarImages := make([]string, 0)
			for index, raw := range asSlice(web["visuallySimilarImages"]) {
				if index >= 5 {
					break
				}
				similarImages = append(
					similarImages, asString(asMap(raw)["url"]),
				)
			}
			pages := make([]map[string]any, 0)
			for index, raw := range asSlice(web["pagesWithMatchingImages"]) {
				if index >= 5 {
					break
				}
				page := asMap(raw)
				pages = append(pages, map[string]any{
					"url":   asString(page["url"]),
					"title": asString(page["pageTitle"]),
				})
			}
			return map[string]any{
				"web_entities":     webEntities,
				"similar_images":   similarImages,
				"pages_with_image": pages,
			}
		},
	)
}

func visionSafeSearch(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	return visionRun(
		ctx, rt, input,
		visionFeature("SAFE_SEARCH_DETECTION", 0),
		func(result map[string]any) any {
			safe := asMap(result["safeSearchAnnotation"])
			return map[string]any{
				"adult":    stringDefault(safe["adult"], "UNKNOWN"),
				"spoof":    stringDefault(safe["spoof"], "UNKNOWN"),
				"medical":  stringDefault(safe["medical"], "UNKNOWN"),
				"violence": stringDefault(safe["violence"], "UNKNOWN"),
				"racy":     stringDefault(safe["racy"], "UNKNOWN"),
			}
		},
	)
}
