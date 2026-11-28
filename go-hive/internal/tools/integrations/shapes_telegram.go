package integrations

// Telegram's Bot API answers every call with {"ok":true,"result":...}; the
// upstream tool returns that envelope whole except get_chat_member_count,
// which projects the scalar result into {"chat_id","member_count"}.
func telegramShapes() map[string]shapeFunc {
	shapes := map[string]shapeFunc{}
	for _, action := range []string{
		"telegram_send_message",
		"telegram_send_document",
		"telegram_edit_message",
		"telegram_delete_message",
		"telegram_forward_message",
		"telegram_send_photo",
		"telegram_send_chat_action",
		"telegram_get_chat",
		"telegram_pin_message",
		"telegram_unpin_message",
		"telegram_send_video",
		"telegram_set_chat_description",
	} {
		shapes[action] = rawShape
	}
	shapes["telegram_get_chat_member_count"] = telegramMemberCount
	return shapes
}

// telegramMemberCount mirrors:
//
//	count = result.get("result", 0) if isinstance(result, dict) else result
//	return {"chat_id": chat_id, "member_count": count}
func telegramMemberCount(parsed any, input map[string]any) any {
	var count any = float64(0)
	if object, ok := parsed.(map[string]any); ok {
		count = object["result"]
		if count == nil {
			count = float64(0)
		}
	} else if parsed != nil {
		count = parsed
	}
	return map[string]any{
		"chat_id":      shapeText(input["chat_id"]),
		"member_count": count,
	}
}
