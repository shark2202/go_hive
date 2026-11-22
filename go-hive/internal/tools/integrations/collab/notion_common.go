// Shared batch-4 helpers (Notion and Linear argument handling), the Notion
// block-type model and the Notion transport/projection helpers.
package collab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	notionVersion  = "2022-06-28"
	notionMaxBody  = 8 << 20
	notionMaxError = 500
	notionMaxItems = 100
)

// ------------------------------------------------------------ shared bits ---

// notionOptionalBool distinguishes an absent/null argument from false.
func notionOptionalBool(input map[string]any, name string) (bool, bool) {
	value, present := input[name]
	if !present || value == nil {
		return false, false
	}
	flag, ok := value.(bool)
	if !ok {
		return false, false
	}
	return flag, true
}

// notionOptionalStrings distinguishes an absent/null list from an empty list.
func notionOptionalStrings(input map[string]any, name string) ([]string, bool) {
	value, present := input[name]
	if !present || value == nil {
		return nil, false
	}
	return stringSliceArg(input, name), true
}

// ---------------------------------------------------------------- notion ----

type notionBlockType string

const (
	notionBlockBulletedItem notionBlockType = "bulleted_list_item"
	notionBlockCallout      notionBlockType = "callout"
	notionBlockHeading1     notionBlockType = "heading_1"
	notionBlockHeading2     notionBlockType = "heading_2"
	notionBlockHeading3     notionBlockType = "heading_3"
	notionBlockNumberedItem notionBlockType = "numbered_list_item"
	notionBlockParagraph    notionBlockType = "paragraph"
	notionBlockQuote        notionBlockType = "quote"
	notionBlockToDo         notionBlockType = "to_do"
)

// notionBlockTypes mirrors Python's sorted(BlockType) order.
var notionBlockTypes = []notionBlockType{
	notionBlockBulletedItem,
	notionBlockCallout,
	notionBlockHeading1,
	notionBlockHeading2,
	notionBlockHeading3,
	notionBlockNumberedItem,
	notionBlockParagraph,
	notionBlockQuote,
	notionBlockToDo,
}

func notionParseBlockType(value string) (notionBlockType, bool) {
	for _, candidate := range notionBlockTypes {
		if string(candidate) == value {
			return candidate, true
		}
	}
	return "", false
}

func notionBlockTypeHelp() string {
	names := make([]string, 0, len(notionBlockTypes))
	for _, blockType := range notionBlockTypes {
		names = append(names, string(blockType))
	}
	return strings.Join(names, ", ")
}

func notionInvalidBlockType(value string) map[string]any {
	return map[string]any{
		"error": fmt.Sprintf("Invalid block_type: '%s'", value),
		"help":  "Must be one of: " + notionBlockTypeHelp(),
	}
}

func notionAuthError() map[string]any {
	return map[string]any{
		"error": "NOTION_API_TOKEN not set",
		"help":  "Create an integration at https://www.notion.so/my-integrations",
	}
}

// notionRequest performs one Notion API call and maps HTTP failures to
// the upstream error dicts.
func notionRequest(ctx context.Context, rt *Runtime, method, path string, payload any) map[string]any {
	token := resolveAccountToken(rt, "notion_tool", "notion")
	if token == "" {
		return notionAuthError()
	}
	base := moduleBaseURL(rt, "notion_tool")
	if base == "" {
		base = "https://api.notion.com"
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return map[string]any{"error": "Notion request failed: " + err.Error()}
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := runtimeHTTP(rt).Do(req)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "Request to Notion timed out"}
		}
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, notionMaxBody))
	if err != nil {
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return map[string]any{"error": "Unauthorized. Check your Notion integration token."}
	case http.StatusForbidden:
		return map[string]any{"error": "Forbidden. Ensure the page/database is shared with the integration."}
	case http.StatusNotFound:
		return map[string]any{"error": "Not found. The page or database may not exist or not be shared."}
	case http.StatusTooManyRequests:
		return map[string]any{"error": "Rate limited. Try again shortly."}
	case http.StatusOK, http.StatusCreated:
		// Success: fall through to decoding.
	default:
		return map[string]any{
			"error": fmt.Sprintf("Notion API error %d: %s", resp.StatusCode, truncateText(string(raw), notionMaxError)),
		}
	}

	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return map[string]any{"error": "Notion request failed: " + err.Error()}
	}
	if object == nil {
		object = map[string]any{}
	}
	return object
}

// notionRichText concatenates rich_text text.content values.
func notionRichText(value any) string {
	var builder strings.Builder
	for _, raw := range asSlice(value) {
		part := asMap(raw)
		text := asMap(part["text"])
		builder.WriteString(asString(text["content"]))
	}
	return builder.String()
}

// notionTitle extracts the first title property's plain text.
func notionTitle(properties map[string]any) string {
	for _, raw := range properties {
		prop := asMap(raw)
		if asString(prop["type"]) == "title" {
			return notionRichText(prop["title"])
		}
	}
	return ""
}

// notionSimpleProperties flattens page properties the way upstream does.
func notionSimpleProperties(properties map[string]any) map[string]any {
	simple := make(map[string]any, len(properties))
	for name, raw := range properties {
		prop := asMap(raw)
		switch asString(prop["type"]) {
		case "title":
			simple[name] = notionRichText(prop["title"])
		case "rich_text":
			simple[name] = notionRichText(prop["rich_text"])
		case "select":
			selectProp := asMap(prop["select"])
			if len(selectProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = asString(selectProp["name"])
			}
		case "multi_select":
			names := make([]string, 0)
			for _, option := range asSlice(prop["multi_select"]) {
				names = append(names, asString(asMap(option)["name"]))
			}
			simple[name] = names
		case "number":
			simple[name] = prop["number"]
		case "checkbox":
			simple[name] = asBool(prop["checkbox"])
		case "date":
			dateProp := asMap(prop["date"])
			if len(dateProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = asString(dateProp["start"])
			}
		case "status":
			statusProp := asMap(prop["status"])
			if len(statusProp) == 0 {
				simple[name] = ""
			} else {
				simple[name] = asString(statusProp["name"])
			}
		}
	}
	return simple
}

// notionDecodeJSONObject decodes a user-supplied JSON object parameter.
func notionDecodeJSONObject(text string) (map[string]any, error) {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("expected a JSON object")
	}
	return object, nil
}
