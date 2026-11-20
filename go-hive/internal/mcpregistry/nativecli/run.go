package nativecli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// UnsupportedError reports an mcp2cli feature outside the MCP subset this
// package implements. Callers use it to fall back to the reference binary.
type UnsupportedError struct {
	Feature string
}

func (e *UnsupportedError) Error() string {
	return "mcp2cli " + e.Feature + " is not supported by the native implementation"
}

// Invocation is everything Run needs from the process.
type Invocation struct {
	Args   []string         // mcp2cli argv, without the program name
	Env    []string         // full environment; nil inherits os.Environ()
	Dir    string           // working directory for a stdio server
	Stdin  io.Reader        // defaults to os.Stdin
	Stdout io.Writer        // defaults to os.Stdout
	Stderr io.Writer        // defaults to os.Stderr
	Now    func() time.Time // clock override for tests
}

// runner carries the state of one CLI invocation.
type runner struct {
	options   *Options
	inv       Invocation
	cacheDir  string
	cacheKey  string
	srcHash   string
	authHeads []Field
	envVars   []Field
	roots     []string
}

// Run executes one mcp2cli invocation and returns its exit code. A non-nil
// error is a setup failure the caller reports (typically UnsupportedError).
func Run(inv Invocation) (int, error) {
	if inv.Stdin == nil {
		inv.Stdin = os.Stdin
	}
	if inv.Stdout == nil {
		inv.Stdout = os.Stdout
	}
	if inv.Stderr == nil {
		inv.Stderr = os.Stderr
	}
	if inv.Env == nil {
		inv.Env = os.Environ()
	}
	if inv.Now == nil {
		inv.Now = time.Now
	}

	globalArgv, toolArgv := splitAtSubcommand(inv.Args)
	options, leftover, err := parseGlobal(globalArgv)
	if err != nil {
		var usage *usageError
		if errors.As(err, &usage) {
			fmt.Fprint(inv.Stderr, usage.render())
			return 2, nil
		}
		return 1, err
	}

	if options.Version {
		fmt.Fprintf(inv.Stdout, "%s %s\n", progName, Version)
		return 0, nil
	}
	if err := unsupportedFeature(options); err != nil {
		return 1, err
	}

	remaining := append(append([]string{}, leftover...), toolArgv...)
	r := &runner{options: options, inv: inv}

	if options.MCPServerURL == "" && options.MCPStdio == "" {
		fmt.Fprint(inv.Stdout, renderMainHelp(nil, defaultGlobalRows()))
		for _, arg := range remaining {
			if arg == "-h" || arg == "--help" {
				return 0, nil
			}
		}
		fmt.Fprint(inv.Stderr, "\nError: one of --mcp or --mcp-stdio is required.\n")
		return 1, nil
	}
	if options.MCPServerURL != "" && options.MCPStdio != "" {
		fmt.Fprint(inv.Stderr, "Error: --mcp and --mcp-stdio are mutually exclusive.\n")
		return 1, nil
	}

	authHeaders, err := r.resolveAuthHeaders()
	if err != nil {
		fmt.Fprintf(inv.Stderr, "%s\n", err)
		return 1, nil
	}
	envVars, err := r.resolveEnvVars()
	if err != nil {
		fmt.Fprintf(inv.Stderr, "%s\n", err)
		return 1, nil
	}
	r.authHeads = authHeaders
	r.envVars = envVars

	roots, err := normalizeRoots(options.Roots)
	if err != nil {
		fmt.Fprintf(inv.Stderr, "Error: %v\n", err)
		return 1, nil
	}
	r.roots = roots

	r.cacheDir = defaultCacheDir(inv.Env)
	r.srcHash = sourceHash(r.source())
	r.cacheKey = options.CacheKey
	if r.cacheKey == "" {
		r.cacheKey = cacheKeyFor(r.source(), r.authHeads, options.Transport, r.envVars, options.MCPStdio != "")
	}

	// --search implies --list.
	if options.Search != "" {
		options.List = true
	}
	code, err := r.dispatch(remaining)
	if err != nil {
		var unsupported *UnsupportedError
		if errors.As(err, &unsupported) {
			return 1, err
		}
		reportMCPError(inv.Stderr, r.source(), err)
		return 1, nil
	}
	return code, nil
}

// unsupportedFeature refuses the mcp2cli modes this package does not cover.
func unsupportedFeature(options *Options) error {
	unsupported := []struct {
		set     bool
		feature string
	}{
		{options.Spec != "", "--spec (OpenAPI)"},
		{options.GraphQL != "", "--graphql"},
		{options.Complete != "", "--complete"},
		{options.Toon, "--toon"},
		{options.Fields != "", "--fields"},
		{options.OAuth || options.OAuthOther, "--oauth"},
		{options.SessionMode, "--session"},
		{options.BaseURL != "", "--base-url"},
	}
	for _, item := range unsupported {
		if item.set {
			return &UnsupportedError{Feature: item.feature}
		}
	}
	return nil
}

// Supports reports whether the native implementation can handle argv.
func Supports(args []string) error {
	globalArgv, _ := splitAtSubcommand(args)
	options, _, err := parseGlobal(globalArgv)
	if err != nil {
		var usage *usageError
		if errors.As(err, &usage) {
			return nil // the native parser produces the same argparse error
		}
		return err
	}
	return unsupportedFeature(options)
}

func (r *runner) source() string {
	if r.options.MCPStdio != "" {
		return r.options.MCPStdio
	}
	return r.options.MCPServerURL
}

// resolveAuthHeaders parses --auth-header values, resolving env: and file:.
func (r *runner) resolveAuthHeaders() ([]Field, error) {
	headers := make([]Field, 0, len(r.options.AuthHeaders))
	for _, item := range r.options.AuthHeaders {
		if !strings.Contains(item, ":") {
			return nil, fmt.Errorf("Error: invalid auth header format: %s", pyRepr(item))
		}
		name, value, _ := strings.Cut(item, ":")
		resolved, err := resolveSecret(strings.TrimSpace(value), r.inv.Env)
		if err != nil {
			return nil, err
		}
		headers = append(headers, Field{Key: strings.TrimSpace(name), Value: resolved})
	}
	return headers, nil
}

// resolveEnvVars parses --env KEY=VALUE values.
func (r *runner) resolveEnvVars() ([]Field, error) {
	vars := make([]Field, 0, len(r.options.Env))
	for _, item := range r.options.Env {
		if !strings.Contains(item, "=") {
			return nil, fmt.Errorf("Error: invalid env format: %s", pyRepr(item))
		}
		key, value, _ := strings.Cut(item, "=")
		vars = append(vars, Field{Key: strings.TrimSpace(key), Value: strings.TrimSpace(value)})
	}
	return vars, nil
}

// resolveSecret expands mcp2cli's env: and file: prefixes.
func resolveSecret(value string, environ []string) (string, error) {
	if strings.HasPrefix(value, "env:") {
		name := value[len("env:"):]
		resolved := ""
		found := false
		for _, entry := range environ {
			key, candidate, ok := strings.Cut(entry, "=")
			if ok && key == name {
				resolved = candidate // last assignment wins, like exec
				found = true
			}
		}
		if !found {
			return "", fmt.Errorf("Error: environment variable %s is not set", pyRepr(name))
		}
		return resolved, nil
	}
	if strings.HasPrefix(value, "file:") {
		path := value[len("file:"):]
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("Error: secret file not found: %s", path)
		}
		return trimTrailingNewline(string(raw)), nil
	}
	return value, nil
}

// dispatch routes to the list, resource, prompt or tool-call flow.
func (r *runner) dispatch(remaining []string) (int, error) {
	resourceAction, resourceURI, promptAction, promptName, promptArgs := r.resolveActions()
	if resourceAction != "" || promptAction != "" {
		return r.runAction(resourceAction, resourceURI, promptAction, promptName, promptArgs)
	}
	if r.options.List {
		return r.runList()
	}
	return r.runTool(remaining)
}

// resolveActions mirrors mcp2cli's _resolve_resource_prompt_actions.
func (r *runner) resolveActions() (string, string, string, string, Ordered) {
	resourceAction, resourceURI := "", ""
	switch {
	case r.options.ListResources:
		resourceAction = "list"
	case r.options.ListTemplates:
		resourceAction = "templates"
	case r.options.ReadResource != "":
		resourceAction = "read"
		resourceURI = r.options.ReadResource
	}
	promptAction, promptName := "", ""
	arguments := Ordered{}
	switch {
	case r.options.ListPrompts:
		promptAction = "list"
	case r.options.GetPrompt != "":
		promptAction = "get"
		promptName = r.options.GetPrompt
		for _, item := range r.options.PromptArgs {
			if key, value, found := strings.Cut(item, "="); found {
				arguments = append(arguments, Field{Key: key, Value: value})
			}
		}
	}
	return resourceAction, resourceURI, promptAction, promptName, arguments
}

// runList connects, lists tools and prints them.
func (r *runner) runList() (int, error) {
	return r.withSession(func(sess *session) (int, error) {
		tools, err := sess.listTools()
		if err != nil {
			return 1, err
		}
		commands := extractCommands(tools)
		if r.options.Search != "" {
			commands = filterCommands(commands, r.options.Search)
			if len(commands) == 0 {
				if r.options.JSON {
					r.printCommands(commands)
				} else {
					fmt.Fprintf(r.inv.Stdout, "\nNo tools matching '%s'.\n", r.options.Search)
				}
				return 0, nil
			}
			if !r.options.Compact && !r.options.JSON {
				fmt.Fprintf(r.inv.Stdout, "\nTools matching '%s':\n", r.options.Search)
			}
		} else if !r.options.Compact && !r.options.JSON {
			fmt.Fprint(r.inv.Stdout, "\nAvailable tools:\n")
		}
		r.printCommands(commands)
		return 0, nil
	})
}

// runTool builds the tool subparser (from cache when possible) and calls it.
func (r *runner) runTool(remaining []string) (int, error) {
	tools, err := r.cachedTools()
	if err != nil {
		return 1, err
	}
	commands := extractCommands(tools)

	if len(remaining) == 0 {
		if !r.options.Compact && !r.options.JSON {
			fmt.Fprint(r.inv.Stdout, "Available tools:\n")
		}
		r.printCommands(commands)
		if !r.options.Compact && !r.options.JSON {
			fmt.Fprint(r.inv.Stdout, "\nUse --list for the same output, or provide a subcommand.\n")
		}
		return 0, nil
	}

	if remaining[0] == "-h" || remaining[0] == "--help" {
		fmt.Fprint(r.inv.Stdout, renderCommandHelp(commands))
		return 0, nil
	}

	commandName, toolArgv := remaining[0], remaining[1:]
	var command *Command
	for index := range commands {
		if commands[index].Name == commandName {
			command = &commands[index]
			break
		}
	}
	if command == nil {
		return r.reportUnknownCommand(commands, remaining)
	}

	values, stdin, help, extras, usageFailure := parseToolArgs(command, toolArgv)
	if usageFailure != nil {
		fmt.Fprint(r.inv.Stderr, usageFailure.render())
		return 2, nil
	}
	if help {
		fmt.Fprint(r.inv.Stdout, renderToolHelp(command))
		return 0, nil
	}
	if len(extras) > 0 {
		fmt.Fprint(r.inv.Stderr, mainUsage(commands)+"\n"+progName+": error: unrecognized arguments: "+strings.Join(extras, " ")+"\n")
		return 2, nil
	}

	arguments, err := r.buildArguments(command, values, stdin)
	if err != nil {
		fmt.Fprintf(r.inv.Stderr, "%s\n", err)
		return 1, nil
	}

	code, callErr := r.withSession(func(sess *session) (int, error) {
		result, err := sess.callTool(command.ToolName, arguments)
		if err != nil {
			return 1, err
		}
		return r.renderToolResult(command, result)
	})
	if callErr != nil {
		return code, callErr
	}
	if code == 0 {
		recordUsage(r.cacheDir, r.srcHash, command.ToolName, r.inv.Now())
	}
	return code, nil
}

// reportUnknownCommand reproduces argparse's invalid-choice failure.
func (r *runner) reportUnknownCommand(commands []Command, remaining []string) (int, error) {
	names := make([]string, 0, len(commands))
	for _, command := range commands {
		names = append(names, "'"+command.Name+"'")
	}
	fmt.Fprint(r.inv.Stderr, mainUsage(commands)+"\n")
	fmt.Fprintf(r.inv.Stderr, "%s: error: argument _command: invalid choice: '%s' (choose from %s)\n",
		progName, remaining[0], strings.Join(names, ", "))
	return 2, nil
}

// buildArguments converts parsed flags into the tool's arguments object.
func (r *runner) buildArguments(command *Command, values map[string]any, stdin bool) (Ordered, error) {
	if stdin {
		raw, err := io.ReadAll(r.inv.Stdin)
		if err != nil {
			return nil, err
		}
		return mergeStdinArguments(command, values, string(raw))
	}
	arguments := Ordered{}
	for _, param := range command.Params {
		value, present := values[paramDest(param)]
		if !present || value == nil {
			continue
		}
		arguments = append(arguments, Field{Key: param.Original, Value: coerceValue(value, param.Schema)})
	}
	return arguments, nil
}

// mergeStdinArguments folds explicit flags into the --stdin JSON object.
func mergeStdinArguments(command *Command, values map[string]any, raw string) (Ordered, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("Error: --stdin expects JSON for MCP tool arguments, but stdin was empty.")
	}
	parsed, err := ParseJSON([]byte(raw))
	if err != nil {
		if line, column, ok := jsonErrorPosition(raw, err); ok {
			return nil, fmt.Errorf("Error: invalid JSON on stdin for MCP tool arguments (line %d, column %d).", line, column)
		}
		return nil, fmt.Errorf("Error: invalid JSON on stdin for MCP tool arguments.")
	}
	object, ok := parsed.(Ordered)
	if !ok {
		flags := []string{}
		for _, param := range command.Params {
			if _, present := values[paramDest(param)]; present {
				flags = append(flags, "--"+param.cliName())
			}
		}
		if len(flags) == 0 {
			// mcp2cli hands the value straight to the client library, which
			// rejects a non-object arguments payload before sending it.
			return nil, fmt.Errorf("Error: --stdin expects a JSON object for MCP tool arguments, got %s.", jsonTypeName(parsed))
		}
		return nil, fmt.Errorf("Error: %s can only be merged into a JSON object on --stdin for MCP tool arguments.",
			strings.Join(flags, ", "))
	}
	merged := append(Ordered{}, object...)
	conflicts := []string{}
	for _, param := range command.Params {
		value, present := values[paramDest(param)]
		if !present || value == nil {
			continue
		}
		coerced := coerceValue(value, param.Schema)
		if existing, found := merged.Lookup(param.Original); found && MarshalPy(existing) != MarshalPy(coerced) {
			conflicts = append(conflicts, fmt.Sprintf("--%s %s vs %q: %s",
				param.cliName(), MarshalPy(coerced), param.Original, MarshalPy(existing)))
		}
		merged = setField(merged, param.Original, coerced)
	}
	if len(conflicts) > 0 {
		return nil, fmt.Errorf("Error: flags conflict with the --stdin JSON for MCP tool arguments: %s. Pass each value in one place, or make them agree.",
			strings.Join(conflicts, "; "))
	}
	return merged, nil
}

func setField(object Ordered, key string, value any) Ordered {
	for index := range object {
		if object[index].Key == key {
			object[index].Value = value
			return object
		}
	}
	return append(object, Field{Key: key, Value: value})
}

// cachedTools returns the tool list from cache or by asking the server.
func (r *runner) cachedTools() ([]any, error) {
	if !r.options.Refresh {
		if tools, ok := loadCachedTools(r.cacheDir, r.cacheKey, r.options.CacheTTL, r.inv.Now()); ok {
			return tools, nil
		}
	}
	var tools []any
	code, err := r.withSession(func(sess *session) (int, error) {
		fetched, err := sess.listTools()
		if err != nil {
			return 1, err
		}
		tools = fetched
		return 0, nil
	})
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("failed to list tools")
	}
	if err := saveCachedTools(r.cacheDir, r.cacheKey, tools); err != nil {
		// The cache is an optimisation; a write failure must not fail the run.
		fmt.Fprintf(r.inv.Stderr, "Warning: could not write the tool cache: %v\n", err)
	}
	return tools, nil
}

// runAction handles --list-resources and friends.
func (r *runner) runAction(resourceAction, resourceURI, promptAction, promptName string, promptArgs Ordered) (int, error) {
	return r.withSession(func(sess *session) (int, error) {
		if resourceAction != "" {
			return r.renderResourceAction(sess, resourceAction, resourceURI)
		}
		return r.renderPromptAction(sess, promptAction, promptName, promptArgs)
	})
}

func (r *runner) renderResourceAction(sess *session, action, uri string) (int, error) {
	switch action {
	case "list":
		result, err := sess.call("resources/list", nil)
		if err != nil {
			return 1, err
		}
		items := []any{}
		if object, ok := result.(Ordered); ok {
			if resources, ok := mustLookup(object, "resources").([]any); ok {
				for _, resource := range resources {
					entry, _ := resource.(Ordered)
					items = append(items, Ordered{
						{Key: "name", Value: stringOf(mustLookup(entry, "name"))},
						{Key: "uri", Value: stringOf(mustLookup(entry, "uri"))},
						{Key: "description", Value: stringOf(mustLookup(entry, "description"))},
						{Key: "mimeType", Value: stringOf(mustLookup(entry, "mimeType"))},
					})
				}
			}
		}
		r.outputResult(items)
	case "templates":
		result, err := sess.call("resources/templates/list", nil)
		if err != nil {
			return 1, err
		}
		items := []any{}
		if object, ok := result.(Ordered); ok {
			if templates, ok := mustLookup(object, "resourceTemplates").([]any); ok {
				for _, template := range templates {
					entry, _ := template.(Ordered)
					items = append(items, Ordered{
						{Key: "name", Value: stringOf(mustLookup(entry, "name"))},
						{Key: "uriTemplate", Value: stringOf(mustLookup(entry, "uriTemplate"))},
						{Key: "description", Value: stringOf(mustLookup(entry, "description"))},
						{Key: "mimeType", Value: stringOf(mustLookup(entry, "mimeType"))},
					})
				}
			}
		}
		r.outputResult(items)
	case "read":
		result, err := sess.call("resources/read", Ordered{{Key: "uri", Value: uri}})
		if err != nil {
			return 1, err
		}
		parts := []string{}
		if object, ok := result.(Ordered); ok {
			if contents, ok := mustLookup(object, "contents").([]any); ok {
				for _, content := range contents {
					entry, _ := content.(Ordered)
					if text, found := entry.Lookup("text"); found {
						parts = append(parts, stringOf(text))
					} else if blob, found := entry.Lookup("blob"); found {
						parts = append(parts, stringOf(blob))
					}
				}
			}
		}
		r.outputResult(strings.Join(parts, "\n"))
	}
	return 0, nil
}

func (r *runner) renderPromptAction(sess *session, action, name string, arguments Ordered) (int, error) {
	switch action {
	case "list":
		result, err := sess.call("prompts/list", nil)
		if err != nil {
			return 1, err
		}
		items := []any{}
		if object, ok := result.(Ordered); ok {
			if prompts, ok := mustLookup(object, "prompts").([]any); ok {
				for _, prompt := range prompts {
					entry, _ := prompt.(Ordered)
					args := []any{}
					if values, ok := mustLookup(entry, "arguments").([]any); ok {
						for _, value := range values {
							argument, _ := value.(Ordered)
							required := false
							if flag, ok := mustLookup(argument, "required").(bool); ok {
								required = flag
							}
							args = append(args, Ordered{
								{Key: "name", Value: stringOf(mustLookup(argument, "name"))},
								{Key: "description", Value: stringOf(mustLookup(argument, "description"))},
								{Key: "required", Value: required},
							})
						}
					}
					items = append(items, Ordered{
						{Key: "name", Value: stringOf(mustLookup(entry, "name"))},
						{Key: "description", Value: stringOf(mustLookup(entry, "description"))},
						{Key: "arguments", Value: args},
					})
				}
			}
		}
		r.outputResult(items)
	case "get":
		result, err := sess.call("prompts/get", Ordered{
			{Key: "name", Value: name},
			{Key: "arguments", Value: arguments},
		})
		if err != nil {
			return 1, err
		}
		messages := []any{}
		object, _ := result.(Ordered)
		if values, ok := mustLookup(object, "messages").([]any); ok {
			for _, value := range values {
				message, _ := value.(Ordered)
				content, _ := mustLookup(message, "content").(Ordered)
				text := ""
				if value, found := content.Lookup("text"); found {
					text = stringOf(value)
				} else {
					text = MarshalPy(content)
				}
				messages = append(messages, Ordered{
					{Key: "role", Value: stringOf(mustLookup(message, "role"))},
					{Key: "content", Value: text},
				})
			}
		}
		r.outputResult(Ordered{
			{Key: "description", Value: stringOf(mustLookup(object, "description"))},
			{Key: "messages", Value: messages},
		})
	}
	return 0, nil
}

// renderToolResult prints a CallToolResult the way mcp2cli does.
func (r *runner) renderToolResult(command *Command, result Ordered) (int, error) {
	isError := false
	if flag, ok := result.Lookup("isError"); ok {
		isError, _ = flag.(bool)
	}
	if r.options.JSON {
		r.outputResult(buildCallToolEnvelope(result))
		if isError {
			return 1, nil
		}
		return 0, nil
	}

	content, _ := result.Lookup("content")
	items, _ := content.([]any)
	text := extractContentParts(items, []string{"text", "data"})
	var payload any = text
	if text == "" {
		if structured, found := result.Lookup("structuredContent"); found && structured != nil {
			payload = structured
		}
	}
	if isError {
		message, isString := payload.(string)
		if !isString {
			if payload == nil {
				message = ""
			} else {
				message = MarshalPy(payload)
			}
		}
		if message == "" {
			message = fmt.Sprintf("tool %s reported an error", pyRepr(command.ToolName))
		}
		fmt.Fprintf(r.inv.Stderr, "Error: %s\n", message)
		return 1, nil
	}
	r.outputResult(payload)
	return 0, nil
}

// buildCallToolEnvelope rebuilds the SDK's CallToolResult dump, including the
// null placeholders pydantic emits and the meta alias mcp2cli prints last.
func buildCallToolEnvelope(result Ordered) Ordered {
	content := []any{}
	if values, ok := mustLookup(result, "content").([]any); ok {
		for _, value := range values {
			content = append(content, normalizeContentItem(value))
		}
	}
	structured, hasStructured := result.Lookup("structuredContent")
	if !hasStructured {
		structured = nil
	}
	isError := false
	if flag, ok := result.Lookup("isError"); ok {
		if typed, isBool := flag.(bool); isBool {
			isError = typed
		}
	}
	meta := any(nil)
	if value, found := result.Lookup("_meta"); found {
		meta = value
	} else if value, found := result.Lookup("meta"); found {
		meta = value
	}
	return Ordered{
		{Key: "content", Value: content},
		{Key: "structuredContent", Value: structured},
		{Key: "isError", Value: isError},
		{Key: "meta", Value: meta},
	}
}

// normalizeContentItem orders one content block's fields the way the SDK
// model dumps them.
func normalizeContentItem(value any) any {
	object, ok := value.(Ordered)
	if !ok {
		return value
	}
	kind := stringOf(mustLookup(object, "type"))
	meta := any(nil)
	if raw, found := object.Lookup("_meta"); found {
		meta = raw
	} else if raw, found := object.Lookup("meta"); found {
		meta = raw
	}
	annotations := any(nil)
	if raw, found := object.Lookup("annotations"); found {
		annotations = raw
	}
	switch kind {
	case "text":
		return Ordered{
			{Key: "type", Value: kind},
			{Key: "text", Value: mustLookup(object, "text")},
			{Key: "annotations", Value: annotations},
			{Key: "meta", Value: meta},
		}
	case "image", "audio":
		return Ordered{
			{Key: "type", Value: kind},
			{Key: "data", Value: mustLookup(object, "data")},
			{Key: "mimeType", Value: mustLookup(object, "mimeType")},
			{Key: "annotations", Value: annotations},
			{Key: "meta", Value: meta},
		}
	case "resource":
		return Ordered{
			{Key: "type", Value: kind},
			{Key: "resource", Value: mustLookup(object, "resource")},
			{Key: "annotations", Value: annotations},
			{Key: "meta", Value: meta},
		}
	case "resource_link":
		return Ordered{
			{Key: "type", Value: kind},
			{Key: "uri", Value: mustLookup(object, "uri")},
			{Key: "name", Value: mustLookup(object, "name")},
			{Key: "description", Value: mustLookup(object, "description")},
			{Key: "mimeType", Value: mustLookup(object, "mimeType")},
			{Key: "size", Value: mustLookup(object, "size")},
			{Key: "annotations", Value: annotations},
			{Key: "meta", Value: meta},
		}
	}
	return value
}

// extractContentParts joins text/data fields, rendering resource links as
// "name: uri".
func extractContentParts(items []any, attributes []string) string {
	parts := []string{}
	for _, item := range items {
		object, ok := item.(Ordered)
		if !ok {
			continue
		}
		found := false
		for _, attribute := range attributes {
			if value, exists := object.Lookup(attribute); exists && value != nil {
				parts = append(parts, stringOf(value))
				found = true
				break
			}
		}
		if found {
			continue
		}
		if uri, exists := object.Lookup("uri"); exists && uri != nil {
			if name, hasName := object.Lookup("name"); hasName && stringOf(name) != "" {
				parts = append(parts, stringOf(name)+": "+stringOf(uri))
			} else {
				parts = append(parts, stringOf(uri))
			}
		}
	}
	return strings.Join(parts, "\n")
}

// outputResult renders a value the way mcp2cli's output_result does.
func (r *runner) outputResult(data any) {
	if r.options.JSON {
		if text, ok := data.(string); ok {
			if parsed, err := ParseJSON([]byte(text)); err == nil {
				data = parsed
			}
		}
		if r.options.Head >= 0 {
			data = applyHead(data, r.options.Head)
		}
		r.emitJSON(data)
		return
	}
	if r.options.Raw {
		if text, ok := data.(string); ok {
			fmt.Fprintln(r.inv.Stdout, text)
			return
		}
		fmt.Fprintln(r.inv.Stdout, MarshalPy(data))
		return
	}
	if text, ok := data.(string); ok {
		parsed, err := ParseJSON([]byte(text))
		if err != nil {
			fmt.Fprintln(r.inv.Stdout, text)
			return
		}
		data = parsed
	}
	if r.options.Head >= 0 {
		data = applyHead(data, r.options.Head)
	}
	r.emitJSON(data)
}

// emitJSON prints JSON, indented when --pretty or when stdout is a terminal.
func (r *runner) emitJSON(data any) {
	if r.options.Pretty || isTerminal(r.inv.Stdout) {
		fmt.Fprintln(r.inv.Stdout, MarshalPyPretty(data))
		return
	}
	fmt.Fprintln(r.inv.Stdout, MarshalPy(data))
}

// applyHead truncates arrays to their first N elements.
func applyHead(data any, n int) any {
	if items, ok := data.([]any); ok {
		if n < len(items) {
			return items[:n]
		}
	}
	return data
}

// isTerminal reports whether the writer is a character device.
func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// filterCommands keeps commands whose name or description contains pattern.
func filterCommands(commands []Command, pattern string) []Command {
	lowered := strings.ToLower(pattern)
	kept := []Command{}
	for _, command := range commands {
		if strings.Contains(strings.ToLower(command.Name), lowered) ||
			strings.Contains(strings.ToLower(command.Description), lowered) {
			kept = append(kept, command)
		}
	}
	return kept
}

// printCommands renders a tool list in text, compact or JSON form.
func (r *runner) printCommands(commands []Command) {
	commands = sortCommands(commands, resolveSortMode(r.options.Sort, r.cacheDir, r.srcHash), r.cacheDir, r.srcHash)
	if r.options.Top >= 0 && r.options.Top < len(commands) {
		commands = commands[:r.options.Top]
	}
	if r.options.JSON {
		payload := make([]any, 0, len(commands))
		if r.options.Compact {
			for _, command := range commands {
				payload = append(payload, command.Name)
			}
		} else {
			for _, command := range commands {
				payload = append(payload, commandToOrdered(command))
			}
		}
		r.emitJSON(payload)
		return
	}
	if r.options.Compact {
		names := make([]string, 0, len(commands))
		for _, command := range commands {
			names = append(names, command.Name)
		}
		fmt.Fprintln(r.inv.Stdout, strings.Join(names, " "))
		return
	}
	for _, command := range commands {
		description := ""
		if command.Description != "" {
			if r.options.Verbose {
				description = "  " + wrapDescription(command.Description, 42, 110)
			} else {
				description = "  " + truncateDescription(command.Description, 70)
			}
		}
		fmt.Fprintf(r.inv.Stdout, "  %-40s%s\n", command.Name, description)
	}
}

// commandToOrdered serializes a command for --list --json.
func commandToOrdered(command Command) Ordered {
	parameters := make([]any, 0, len(command.Params))
	for _, param := range command.Params {
		parameters = append(parameters, Ordered{
			{Key: "name", Value: param.cliName()},
			{Key: "type", Value: param.Kind.typeName()},
			{Key: "required", Value: param.Required},
			{Key: "description", Value: param.Description},
			{Key: "location", Value: "tool_input"},
		})
	}
	entry := Ordered{
		{Key: "name", Value: command.Name},
		{Key: "description", Value: command.Description},
	}
	if command.ToolName != "" {
		entry = append(entry, Field{Key: "toolName", Value: command.ToolName})
	}
	return append(entry, Field{Key: "parameters", Value: parameters})
}

// withSession dials the server, initializes, runs fn and closes the
// connection, retrying over SSE when the transport is auto.
func (r *runner) withSession(fn func(*session) (int, error)) (int, error) {
	if r.options.MCPStdio != "" {
		return r.withStdioSession(fn, false)
	}
	headers := r.authHeads
	switch r.options.Transport {
	case "sse":
		return r.withSSESession(fn, headers)
	case "streamable":
		return r.withStreamableSession(fn, headers)
	default:
		code, err := r.withStreamableSession(fn, headers)
		if err == nil {
			return code, nil
		}
		return r.withSSESession(fn, headers)
	}
}

func (r *runner) withStreamableSession(fn func(*session) (int, error), headers []Field) (int, error) {
	transport := newStreamableTransport(r.options.MCPServerURL, headers)
	defer transport.Close()
	return r.runSession(transport, fn)
}

func (r *runner) withSSESession(fn func(*session) (int, error), headers []Field) (int, error) {
	transport, err := newSSETransport(r.options.MCPServerURL, headers)
	if err != nil {
		return 1, err
	}
	defer transport.Close()
	return r.runSession(transport, fn)
}

// withStdioSession spawns the server; a server that answers with LSP framing
// is retried once with that framing from the first message.
func (r *runner) withStdioSession(fn func(*session) (int, error), headerFraming bool) (int, error) {
	transport, err := newStdioTransport(r.options.MCPStdio, mergedEnv(r.inv.Env, r.envVars), r.inv.Dir, r.inv.Stderr)
	if err != nil {
		return 1, err
	}
	defer transport.Close()
	transport.writeHeader = headerFraming
	code, err := r.runSession(transport, fn)
	if err != nil && !headerFraming && isFramingFailure(err) {
		return r.withStdioSession(fn, true)
	}
	return code, err
}

func (r *runner) runSession(transport transport, fn func(*session) (int, error)) (int, error) {
	sess := &session{transport: transport, source: r.source(), roots: r.roots}
	if err := sess.initialize(); err != nil {
		return 1, err
	}
	return fn(sess)
}

// isFramingFailure reports whether a stdio exchange failed in a way that
// suggests the server speaks Content-Length framing.
func isFramingFailure(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	return strings.Contains(err.Error(), "invalid JSON-RPC message")
}

// flattenError renders nested errors as a single terminal-safe line.
func flattenError(err error) error {
	message := strings.TrimSpace(strings.Join(strings.Fields(err.Error()), " "))
	if message == "" {
		return errors.New("unknown error")
	}
	return errors.New(message)
}

// reportMCPError prints mcp2cli's cleaned-up transport failure.
func reportMCPError(stderr io.Writer, source string, err error) {
	message := flattenError(err).Error()
	lowered := strings.ToLower(message)
	hint := ""
	switch {
	case strings.Contains(lowered, "401") || strings.Contains(lowered, "403"):
		hint = " — the server rejected the request; pass credentials with --auth-header 'Name:Value' or use the --oauth-* options"
	case errors.Is(err, io.EOF) || strings.Contains(message, "Connection closed") || strings.Contains(message, "unexpected EOF"):
		hint = " — the server disconnected unexpectedly; it may have crashed or exited. Check the server command and its logs"
	}
	fmt.Fprintf(stderr, "Error: cannot use MCP server at %s: %s%s\n", source, message, hint)
}

// jsonErrorPosition converts a json.SyntaxError offset into a 1-based
// line/column pair, the way Python's json module reports it.
func jsonErrorPosition(raw string, err error) (int, int, bool) {
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		return 0, 0, false
	}
	offset := int(syntax.Offset)
	if offset > len(raw) {
		offset = len(raw)
	}
	if offset < 1 {
		offset = 1
	}
	// Go points at the last byte it consumed; Python points at the offending
	// character, so skip whitespace to land on the same position.
	position := offset - 1
	for position < len(raw) {
		switch raw[position] {
		case ' ', '\t', '\r', '\n':
			position++
			continue
		}
		break
	}
	if position > len(raw) {
		position = len(raw)
	}
	line, column := 1, 1
	for index := 0; index < position; index++ {
		if raw[index] == '\n' {
			line++
			column = 1
			continue
		}
		column++
	}
	return line, column, true
}

// jsonTypeName names a decoded JSON value for error messages.
func jsonTypeName(value any) string {
	switch value.(type) {
	case []any:
		return "an array"
	case string:
		return "a string"
	case json.Number:
		return "a number"
	case bool:
		return "a boolean"
	case nil:
		return "null"
	default:
		return "an unsupported value"
	}
}
