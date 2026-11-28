package agentloop

import "encoding/json"

// OutputAccumulator reconstructs the upstream OutputAccumulator state from the
// conversation: every successful `set_output` call for a declared key, in call
// order, with later calls overriding earlier ones. It is the Go port's clean
// accumulator (upstream persists these values in the cursor; the port's
// set_output runs through the host executor, so the stored call/result pair is
// the source of truth).
//
// A call whose result is missing (the in-flight assistant turn) or is an error
// does not set the key, mirroring handle_set_output's validation. The live loop
// rejects `set_output` outright (upstream removed the tool), so this view is
// empty for such a call: the judge gate must keep reporting the key as missing
// even though the checkpoint keeps the call's value for resume
// (see OutputCallValues).
//
// Parity: framework/agent_loop/internals/types.py::OutputAccumulator
// Parity: framework/agent_loop/agent_loop.py::_get_missing_output_keys
func (c *NodeConversation) OutputAccumulator(outputKeys []string) map[string]any {
	return c.outputValues(outputKeys, true)
}

// OutputCallValues returns the value every `set_output` call carried for a
// declared key, in call order with later calls overriding earlier ones, whether
// or not the stored result succeeded.
//
// The live loop rejects `set_output` before dispatch (upstream removed it), so a
// live transcript can never carry a successful result; the call's arguments are
// the only durable record of the value the model produced. The cursor
// checkpoint reads them so a resumed run keeps its outputs, while the judge
// gate keeps using OutputAccumulator's success-only view.
func (c *NodeConversation) OutputCallValues(outputKeys []string) map[string]any {
	return c.outputValues(outputKeys, false)
}

// outputValues is the shared transcript scan behind both views. successfulOnly
// keeps just the calls whose stored result is a non-error (the live
// accumulator); otherwise every declared-key call counts.
func (c *NodeConversation) outputValues(outputKeys []string, successfulOnly bool) map[string]any {
	values := map[string]any{}
	if c == nil || len(outputKeys) == 0 {
		return values
	}
	allowed := make(map[string]struct{}, len(outputKeys))
	for _, key := range outputKeys {
		allowed[key] = struct{}{}
	}
	for index, message := range c.messages {
		if message.Role != RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			fn, _ := call["function"].(map[string]any)
			if fn == nil {
				continue
			}
			if name, _ := fn["name"].(string); name != "set_output" {
				continue
			}
			args := toolCallArguments(fn["arguments"])
			key, _ := args["key"].(string)
			if key == "" {
				continue
			}
			if _, ok := allowed[key]; !ok {
				continue
			}
			if successfulOnly {
				id, _ := call["id"].(string)
				if id == "" || !toolResultSucceeded(c.messages[index+1:], id) {
					continue
				}
			}
			value, present := args["value"]
			if !present || value == nil {
				value = ""
			}
			values[key] = value
		}
	}
	return values
}

// toolCallArguments normalizes a tool call's `arguments` field, which the
// provider layer stores as a JSON string and tests sometimes build as a map.
func toolCallArguments(raw any) map[string]any {
	switch value := raw.(type) {
	case map[string]any:
		return value
	case string:
		if value == "" {
			return nil
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(value), &args); err != nil {
			return nil
		}
		return args
	default:
		return nil
	}
}

// toolResultSucceeded reports whether the stored tool result for toolUseID
// exists and is not an error.
func toolResultSucceeded(messages []Message, toolUseID string) bool {
	for _, message := range messages {
		if message.Role == RoleTool && message.ToolUseID != nil && *message.ToolUseID == toolUseID {
			return !message.IsError
		}
	}
	return false
}
