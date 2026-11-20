package nativecli

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// paramKind is the argparse type a JSON Schema property maps onto.
type paramKind int

const (
	kindString paramKind = iota // argparse type=str
	kindInt                     // argparse type=int
	kindFloat                   // argparse type=float
	kindBool                    // argparse action=store_true
	kindUnion                   // anyOf/oneOf with a numeric branch
)

func (k paramKind) typeName() string {
	switch k {
	case kindInt:
		return "int"
	case kindFloat:
		return "float"
	case kindBool:
		return "boolean"
	default:
		return "str"
	}
}

// Param is one tool parameter, derived from the tool's inputSchema.
type Param struct {
	Name        string // kebab-case flag name
	Original    string // wire name in the arguments object
	Kind        paramKind
	Required    bool
	Description string
	Schema      any
	CLIName     string // collision-free flag name (Name unless it shadows --help/--stdin)
}

// Command is one MCP tool exposed as a CLI subcommand.
type Command struct {
	Name        string
	Description string
	Params      []Param
	HasBody     bool
	ToolName    string
}

// paramDest is the argparse destination: the flag name with dashes replaced.
func paramDest(p Param) string {
	return strings.ReplaceAll(p.cliName(), "-", "_")
}

func (p Param) cliName() string {
	if p.CLIName != "" {
		return p.CLIName
	}
	return p.Name
}

var kebabUpperRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// toKebab converts a wire tool/parameter name to a CLI flag name.
func toKebab(name string) string {
	return strings.ToLower(strings.ReplaceAll(kebabUpperRe.ReplaceAllString(name, `${1}-${2}`), "_", "-"))
}

// normalizeSchemaType reduces a JSON Schema "type" to one concrete name,
// dropping "null" from the ["integer", "null"] array form.
func normalizeSchemaType(value any) string {
	if list, ok := value.([]any); ok {
		concrete := make([]string, 0, len(list))
		for _, item := range list {
			if text, ok := item.(string); ok && text != "null" {
				concrete = append(concrete, text)
			}
		}
		if len(concrete) == 1 {
			return concrete[0]
		}
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// collapseNullable unwraps the Pydantic Optional[T] shape
// ({"anyOf": [T, {"type": "null"}]}) to T while keeping outer keys.
func collapseNullable(schema any) any {
	object, ok := schema.(Ordered)
	if !ok {
		return schema
	}
	if _, hasType := object.Lookup("type"); hasType {
		return schema
	}
	unionKey := ""
	for _, key := range []string{"anyOf", "oneOf"} {
		if _, found := object.Lookup(key); found {
			if unionKey != "" {
				return schema
			}
			unionKey = key
		}
	}
	if unionKey == "" {
		return schema
	}
	branchesValue, _ := object.Lookup(unionKey)
	branches, ok := branchesValue.([]any)
	if !ok || len(branches) != 2 {
		return schema
	}
	var concrete Ordered
	for _, branch := range branches {
		branchObject, ok := branch.(Ordered)
		if !ok {
			return schema
		}
		if branchType, _ := branchObject.Lookup("type"); branchType == "null" {
			continue
		}
		if concrete != nil {
			return schema
		}
		concrete = branchObject
	}
	if concrete == nil {
		return schema
	}
	merged := Ordered{}
	for _, field := range concrete {
		merged = append(merged, field)
	}
	for _, field := range object {
		if field.Key == unionKey {
			continue
		}
		merged = append(merged, field)
	}
	return merged
}

// unionBranchTypes collects the JSON types an anyOf/oneOf union admits.
func unionBranchTypes(schema Ordered) map[string]bool {
	types := map[string]bool{}
	var alternatives []any
	for _, key := range []string{"anyOf", "oneOf"} {
		if value, found := schema.Lookup(key); found {
			if list, ok := value.([]any); ok && alternatives == nil {
				alternatives = list
			}
		}
	}
	for _, alternative := range alternatives {
		object, ok := alternative.(Ordered)
		if !ok {
			types["other"] = true
			continue
		}
		if value, found := object.Lookup("type"); found {
			switch typed := value.(type) {
			case string:
				types[typed] = true
			case []any:
				for _, item := range typed {
					if text, ok := item.(string); ok {
						types[text] = true
					}
				}
			}
			continue
		}
		if _, nested := object.Lookup("anyOf"); nested {
			for name := range unionBranchTypes(object) {
				types[name] = true
			}
			continue
		}
		if _, nested := object.Lookup("oneOf"); nested {
			for name := range unionBranchTypes(object) {
				types[name] = true
			}
			continue
		}
		types["other"] = true
	}
	return types
}

// schemaToKind maps a JSON Schema node onto an argparse type, matching
// mcp2cli's schema_type_to_python, and returns the description suffix.
func schemaToKind(schema any) (paramKind, string) {
	object, _ := schema.(Ordered)
	if object == nil {
		return kindString, ""
	}
	switch normalizeSchemaType(mustLookup(object, "type")) {
	case "integer":
		return kindInt, ""
	case "number":
		return kindFloat, ""
	case "boolean":
		return kindBool, ""
	case "array":
		return kindString, " (JSON array)"
	case "object":
		return kindString, " (JSON object)"
	}
	if _, hasType := object.Lookup("type"); !hasType {
		if enumValue, found := object.Lookup("enum"); found {
			if items, ok := enumValue.([]any); ok && len(items) > 0 {
				allInt, allNumber := true, true
				for _, item := range items {
					switch typed := item.(type) {
					case bool:
						allInt, allNumber = false, false
					case json.Number:
						if strings.ContainsAny(string(typed), ".eE") {
							allInt = false
						}
					default:
						allInt, allNumber = false, false
					}
				}
				if allInt {
					return kindInt, ""
				}
				if allNumber {
					return kindFloat, ""
				}
			}
		}
		if _, hasAny := object.Lookup("anyOf"); hasAny {
			if kind, ok := unionPreferNumeric(object); ok {
				return kind, ""
			}
		}
		if _, hasOne := object.Lookup("oneOf"); hasOne {
			if kind, ok := unionPreferNumeric(object); ok {
				return kind, ""
			}
		}
	}
	return kindString, ""
}

// unionPreferNumeric picks a numeric converter for a union that admits one.
func unionPreferNumeric(schema Ordered) (paramKind, bool) {
	types := unionBranchTypes(schema)
	numeric := false
	other := false
	for name := range types {
		switch name {
		case "integer", "number", "null":
			numeric = true
		default:
			other = true
		}
	}
	if !numeric {
		return kindString, false
	}
	if !other {
		if types["integer"] && !types["number"] {
			return kindInt, true
		}
		return kindFloat, true
	}
	return kindUnion, true
}

func mustLookup(object Ordered, key string) any {
	value, _ := object.Lookup(key)
	return value
}

// extractCommands turns tools/list entries into commands, de-duplicating
// kebab-case collisions the way mcp2cli does (first natural name wins, later
// collisions get a numeric suffix, and a suffix never shadows another tool's
// natural name).
func extractCommands(tools []any) []Command {
	baseNames := make([]string, 0, len(tools))
	reserved := map[string]bool{}
	for _, tool := range tools {
		object, _ := tool.(Ordered)
		name := "unknown"
		if object != nil {
			if value, ok := object.Lookup("name"); ok {
				if text, ok := value.(string); ok && text != "" {
					name = text
				}
			}
		}
		kebab := toKebab(name)
		baseNames = append(baseNames, kebab)
		reserved[kebab] = true
	}

	cliNames := make([]string, len(tools))
	used := map[string]bool{}
	groups := map[string][]int{}
	order := []string{}
	for index, baseName := range baseNames {
		if _, seen := groups[baseName]; !seen {
			order = append(order, baseName)
		}
		groups[baseName] = append(groups[baseName], index)
	}
	sortStrings(order)
	for _, baseName := range order {
		indices := groups[baseName]
		for rank, index := range indices {
			name := baseName
			if rank > 0 {
				suffix := 2
				name = fmt.Sprintf("%s-%d", baseName, suffix)
				for reserved[name] || used[name] {
					suffix++
					name = fmt.Sprintf("%s-%d", baseName, suffix)
				}
			}
			cliNames[index] = name
			used[name] = true
		}
	}

	commands := make([]Command, 0, len(tools))
	for index, tool := range tools {
		object, _ := tool.(Ordered)
		command := Command{Name: cliNames[index]}
		if object == nil {
			commands = append(commands, command)
			continue
		}
		if value, ok := object.Lookup("description"); ok {
			if text, ok := value.(string); ok {
				command.Description = text
			}
		}
		if value, ok := object.Lookup("name"); ok {
			if text, ok := value.(string); ok {
				command.ToolName = text
			}
		}
		schemaValue, _ := object.Lookup("inputSchema")
		schema, _ := schemaValue.(Ordered)
		required := map[string]bool{}
		if schema != nil {
			if value, ok := schema.Lookup("required"); ok {
				if items, ok := value.([]any); ok {
					for _, item := range items {
						if text, ok := item.(string); ok {
							required[text] = true
						}
					}
				}
			}
			if value, ok := schema.Lookup("properties"); ok {
				if properties, ok := value.(Ordered); ok {
					for _, property := range properties {
						propertySchema := collapseNullable(property.Value)
						kind, suffix := schemaToKind(propertySchema)
						description := property.Key
						if propertyObject, ok := propertySchema.(Ordered); ok {
							if text, ok := mustLookup(propertyObject, "description").(string); ok && text != "" {
								description = text
							}
						}
						command.Params = append(command.Params, Param{
							Name:        toKebab(property.Key),
							Original:    property.Key,
							Kind:        kind,
							Required:    required[property.Key],
							Description: description + suffix,
							Schema:      propertySchema,
						})
					}
				}
			}
		}
		command.HasBody = len(command.Params) > 0
		allocateCLINames(&command)
		commands = append(commands, command)
	}
	return commands
}

// allocateCLINames assigns flags that do not shadow the built-in options.
func allocateCLINames(command *Command) {
	natural := map[string]bool{}
	for _, param := range command.Params {
		natural[param.Name] = true
	}
	used := map[string]bool{"help": true}
	if command.HasBody {
		used["stdin"] = true
	}
	for index := range command.Params {
		name := command.Params[index].Name
		if used[name] {
			stem := "arg-" + name
			name = stem
			suffix := 2
			for used[name] || natural[name] {
				name = fmt.Sprintf("%s-%d", stem, suffix)
				suffix++
			}
		}
		command.Params[index].CLIName = name
		used[name] = true
	}
}

// coerceValue converts a command-line token to the JSON value the tool
// expects, following mcp2cli's coerce_value.
func coerceValue(value any, schema any) any {
	if value == nil {
		return nil
	}
	object, _ := schema.(Ordered)
	schemaType := ""
	if object != nil {
		schemaType = normalizeSchemaType(mustLookup(object, "type"))
	}
	switch schemaType {
	case "array":
		switch typed := value.(type) {
		case []any:
			return typed
		case string:
			if parsed, err := ParseJSON([]byte(typed)); err == nil {
				if list, ok := parsed.([]any); ok {
					return list
				}
			}
			itemType := ""
			if object != nil {
				if items, ok := mustLookup(object, "items").(Ordered); ok {
					itemType = normalizeSchemaType(mustLookup(items, "type"))
				}
			}
			if strings.Contains(typed, ",") {
				parts := strings.Split(typed, ",")
				out := make([]any, 0, len(parts))
				for _, part := range parts {
					out = append(out, coerceItem(strings.TrimSpace(part), itemType))
				}
				return out
			}
			return []any{coerceItem(typed, itemType)}
		default:
			return value
		}
	case "object":
		if text, ok := value.(string); ok {
			if parsed, err := ParseJSON([]byte(text)); err == nil {
				return parsed
			}
			return text
		}
		return value
	case "boolean":
		switch typed := value.(type) {
		case bool:
			return typed
		case string:
			return typed != ""
		default:
			return true
		}
	case "integer":
		if number, ok := value.(json.Number); ok {
			if parsed, err := number.Int64(); err == nil {
				return json.Number(strconv.FormatInt(parsed, 10))
			}
		}
		return value
	case "number":
		return value
	}
	if text, ok := value.(string); ok {
		trimmed := strings.TrimSpace(text)
		if trimmed != "" && (trimmed[0] == '{' || trimmed[0] == '[') {
			if parsed, err := ParseJSON([]byte(trimmed)); err == nil {
				switch parsed.(type) {
				case Ordered, []any:
					return parsed
				}
			}
		}
	}
	return value
}

func coerceItem(value string, itemType string) any {
	switch itemType {
	case "integer":
		if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
			return json.Number(strconv.FormatInt(parsed, 10))
		}
		return value
	case "number":
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil {
			return json.Number(strconv.FormatFloat(parsed, 'g', -1, 64))
		}
		return value
	case "boolean":
		lowered := strings.ToLower(value)
		return lowered == "true" || lowered == "1" || lowered == "yes"
	}
	return value
}

// truncateDescription truncates at a word boundary and appends "...".
func truncateDescription(description string, maxLen int) string {
	if utf8.RuneCountInString(description) <= maxLen {
		return description
	}
	runes := []rune(description)
	head := string(runes[:maxLen])
	if index := strings.LastIndex(head, " "); index >= 0 {
		head = head[:index]
	}
	return strings.TrimRight(head, " ") + "..."
}

// wrapDescription wraps long descriptions, indenting continuation lines to
// line up under the description column (Python textwrap.fill semantics).
func wrapDescription(description string, indent, totalWidth int) string {
	words := splitPyWords(description)
	if len(words) == 0 {
		return strings.Repeat(" ", indent)
	}
	var lines []string
	line := ""
	width := totalWidth
	for _, word := range words {
		switch {
		case line == "":
			line = word
		case len([]rune(line))+1+len([]rune(word)) <= width:
			line += " " + word
		default:
			lines = append(lines, line)
			line = word
			width = totalWidth - indent
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+strings.Repeat(" ", indent))
}

// splitPyWords splits text on whitespace without treating hyphens as breaks.
func splitPyWords(text string) []string {
	return strings.Fields(text)
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
