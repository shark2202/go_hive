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

type ToolResult struct {
	ToolUseID      string
	Content        string
	IsError        bool
	ImageContent   []map[string]any
	IsSkillContent bool
	SpilloverPath  string
}

func NewToolResult(toolUseID, content string) ToolResult {
	return ToolResult{ToolUseID: toolUseID, Content: content}
}

type RegisteredTool struct {
	Tool     Tool
	Executor ToolExecutor
}

type ToolExecutor func(inputs map[string]any) (any, error)
