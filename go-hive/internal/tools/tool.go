package tools

type Tool struct {
	Name            string
	Description     string
	Parameters      map[string]any
	ProducesImage   bool
	ConcurrencySafe bool
}

func NewTool(name, description string) Tool {
	return Tool{Name: name, Description: description, Parameters: map[string]any{}}
}

func (t Tool) Schema() map[string]any {
	params := t.Parameters
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"name":         t.Name,
		"description":  t.Description,
		"input_schema": params,
	}
}

type ToolUse struct {
	ID    string
	Name  string
	Input map[string]any
}

// AttachmentRef is one file a tool surfaced for the user, independent of the
// bytes themselves. A file-backed tool (attach_file) returns these so the
// runtime that owns the session directory can copy the file into the session's
// attachments folder and publish a downloadable chip. Runtimes that cannot
// publish MUST report the reference instead of claiming the file was delivered.
type AttachmentRef struct {
	// Path is the raw path the caller supplied.
	Path string
	// Resolved is the absolute source path that was read.
	Resolved string
	// Kind is one of pdf, image, text, blob — the classification the tool
	// chose for the file.
	Kind string
	// MIME is the content type inferred from the extension or bytes.
	MIME string
	// Bytes is the on-disk size at read time.
	Bytes int64
	// Filename is the base name presented to the user.
	Filename string
}

type ToolResult struct {
	ToolUseID      string
	Content        string
	IsError        bool
	ImageContent   []map[string]any
	IsSkillContent bool
	SpilloverPath  string
	// Attachments lists files the tool wants the user to receive. It is
	// optional and additive: the zero value means "no attachments" and every
	// existing caller keeps its behaviour. Tools that return it also repeat
	// the same metadata in Content, so a text-only transport still tells the
	// model what was attached.
	Attachments []AttachmentRef
}

func NewToolResult(toolUseID, content string) ToolResult {
	return ToolResult{ToolUseID: toolUseID, Content: content}
}

type RegisteredTool struct {
	Tool     Tool
	Executor ToolExecutor
}

type ToolExecutor func(inputs map[string]any) (any, error)
