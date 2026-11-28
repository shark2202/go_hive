package integrations

// Trello's upstream tool names the list arrays it returns ({"boards":...},
// {"lists":...}, {"cards":...}); object endpoints return the provider card /
// list / member document unchanged, and search projects cards + count.
func trelloShapes() map[string]shapeFunc {
	return map[string]shapeFunc{
		"trello_list_boards":    trelloListShape("boards"),
		"trello_get_member":     rawShape,
		"trello_list_lists":     trelloListShape("lists"),
		"trello_list_cards":     trelloListShape("cards"),
		"trello_create_card":    rawShape,
		"trello_move_card":      rawShape,
		"trello_update_card":    rawShape,
		"trello_add_comment":    rawShape,
		"trello_add_attachment": rawShape,
		"trello_get_card":       rawShape,
		"trello_create_list":    rawShape,
		"trello_search_cards":   trelloSearchShape,
	}
}

// trelloListShape mirrors:
//
//	if isinstance(result, list): return {key: result}
//	return result
//
// A provider error object is passed through by the kernel before shaping; a
// successful body is always the bare array on these endpoints.
func trelloListShape(key string) shapeFunc {
	return func(parsed any, _ map[string]any) any {
		if items, ok := parsed.([]any); ok {
			return map[string]any{key: items}
		}
		return parsed
	}
}

// trelloSearchShape mirrors:
//
//	cards = result.get("cards", [])
//	return {"cards": cards, "count": len(cards)}
func trelloSearchShape(parsed any, _ map[string]any) any {
	cards := shapeList(shapeMap(parsed), "cards")
	return map[string]any{"cards": cards, "count": len(cards)}
}
