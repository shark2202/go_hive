// Microsoft Graph v1.0 (upstream microsoft_graph_tool, 11 actions).
//
// One bearer token (MICROSOFT_GRAPH_ACCESS_TOKEN, or WithCredential) covers
// three surfaces:
//
//	me/mailFolders/{folder}/messages   Outlook list
//	me/messages/{id}, me/sendMail      Outlook read/send
//	me/joinedTeams, teams/...          Teams list/read/post
//	me/drive/...                       OneDrive search/list/download/upload
//
// Status mapping follows the upstream helpers: 401 and 403 have dedicated
// messages, 202 answers {"status":"accepted"} for writes, and an empty body on
// a write answers {"status":"success"}. Downloads follow the provider's
// @microsoft.graph.downloadUrl and return text for text/* (plus JSON/XML/JS)
// and standard base64 for everything else.
package integrations

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var batch7Lane9GraphTool = &Tool{
	ID:          "microsoft_graph_tool",
	Name:        "microsoft_graph",
	DisplayName: "Microsoft Graph",
	Summary:     "Outlook mail, Teams messaging and OneDrive files via Microsoft Graph (11 actions).",
	Actions: []Action{
		{
			Name:    "outlook_list_messages",
			Summary: "List email messages from an Outlook mailbox folder.",
			Source:  "microsoft_graph_tool/microsoft_graph_tool.py",
			Params: map[string]any{
				"folder":        paramDefault("string", "inbox"),
				"max_results":   paramDefault("integer", 20),
				"filter_unread": paramDefault("boolean", false),
				"search":        paramDefault("string", ""),
			},
		},
		{
			Name:     "outlook_get_message",
			Summary:  "Get full details of an Outlook email message.",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"message_id"},
			Params: map[string]any{
				"message_id": param("string"),
			},
		},
		{
			Name:     "outlook_send_mail",
			Summary:  "Send an email via Outlook.",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"to", "subject"},
			Params: map[string]any{
				"to":           param("string"),
				"subject":      param("string"),
				"body":         paramDefault("string", ""),
				"body_type":    paramDefault("string", "Text"),
				"cc":           paramDefault("string", ""),
				"save_to_sent": paramDefault("boolean", true),
			},
		},
		{
			Name:    "teams_list_teams",
			Summary: "List all Teams the current user is a member of.",
			Source:  "microsoft_graph_tool/microsoft_graph_tool.py",
			Params:  map[string]any{},
		},
		{
			Name:     "teams_list_channels",
			Summary:  "List channels in a Microsoft Teams team.",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"team_id"},
			Params: map[string]any{
				"team_id": param("string"),
			},
		},
		{
			Name:     "teams_send_channel_message",
			Summary:  "Send a message to a Microsoft Teams channel.",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"team_id", "channel_id", "message"},
			Params: map[string]any{
				"team_id":      param("string"),
				"channel_id":   param("string"),
				"message":      param("string"),
				"content_type": paramDefault("string", "text"),
			},
		},
		{
			Name:     "teams_get_channel_messages",
			Summary:  "Get recent messages from a Microsoft Teams channel.",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"team_id", "channel_id"},
			Params: map[string]any{
				"team_id":     param("string"),
				"channel_id":  param("string"),
				"max_results": paramDefault("integer", 20),
			},
		},
		{
			Name:     "onedrive_search_files",
			Summary:  "Search for files in the user's OneDrive.",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"query"},
			Params: map[string]any{
				"query":       param("string"),
				"max_results": paramDefault("integer", 20),
			},
		},
		{
			Name:    "onedrive_list_files",
			Summary: "List files and folders in a OneDrive directory.",
			Source:  "microsoft_graph_tool/microsoft_graph_tool.py",
			Params: map[string]any{
				"folder_path": paramDefault("string", ""),
				"max_results": paramDefault("integer", 50),
			},
		},
		{
			Name:    "onedrive_download_file",
			Summary: "Download a file from OneDrive (base64 for binary, text for text).",
			Source:  "microsoft_graph_tool/microsoft_graph_tool.py",
			Params: map[string]any{
				"item_id":   paramDefault("string", ""),
				"file_path": paramDefault("string", ""),
			},
		},
		{
			Name:     "onedrive_upload_file",
			Summary:  "Upload a small file to OneDrive (up to 4MB).",
			Source:   "microsoft_graph_tool/microsoft_graph_tool.py",
			Required: []string{"file_path", "content"},
			Params: map[string]any{
				"file_path":    param("string"),
				"content":      param("string"),
				"content_type": paramDefault("string", "text/plain"),
			},
		},
	},
}

func batch7Lane9GraphImpls() map[string]ActionFunc {
	return map[string]ActionFunc{
		"outlook_list_messages":      batch7Lane9GraphListMessages,
		"outlook_get_message":        batch7Lane9GraphGetMessage,
		"outlook_send_mail":          batch7Lane9GraphSendMail,
		"teams_list_teams":           batch7Lane9GraphListTeams,
		"teams_list_channels":        batch7Lane9GraphListChannels,
		"teams_send_channel_message": batch7Lane9GraphSendChannelMessage,
		"teams_get_channel_messages": batch7Lane9GraphChannelMessages,
		"onedrive_search_files":      batch7Lane9GraphSearchFiles,
		"onedrive_list_files":        batch7Lane9GraphListFiles,
		"onedrive_download_file":     batch7Lane9GraphDownloadFile,
		"onedrive_upload_file":       batch7Lane9GraphUploadFile,
	}
}

// batch7Lane9GraphAuthError mirrors upstream _auth_error.
func batch7Lane9GraphAuthError() map[string]any {
	return map[string]any{
		"error": "MICROSOFT_GRAPH_ACCESS_TOKEN not set",
		"help":  "Register an app at https://portal.azure.com/#blade/Microsoft_AAD_RegisteredApps/ApplicationsListBlade",
	}
}

// batch7Lane9GraphRoot renders the Graph base URL.
func batch7Lane9GraphRoot(rt *Runtime) string {
	return marketingBase(rt, "microsoft_graph_tool", batch7Lane9GraphBase)
}

// batch7Lane9GraphToken resolves the bearer token.
func batch7Lane9GraphToken(rt *Runtime) string {
	return moduleToken(rt, "microsoft_graph_tool")
}

// batch7Lane9GraphHeaders builds the JSON request headers.
func batch7Lane9GraphHeaders(rt *Runtime) map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + batch7Lane9GraphToken(rt),
		"Content-Type":  "application/json",
	}
}

// batch7Lane9GraphDecode maps a Graph response to the upstream result shape.
// allowAccepted covers writes: 202 reports accepted and an empty body reports
// success instead of a JSON error.
func batch7Lane9GraphDecode(resp *batch7Lane9Response, allowAccepted bool) (map[string]any, map[string]any) {
	switch resp.Status {
	case http.StatusUnauthorized:
		return nil, map[string]any{"error": "Unauthorized. Access token may be expired or invalid."}
	case http.StatusForbidden:
		return nil, map[string]any{
			"error": "Forbidden. Missing required permission scope. Details: " + truncateText(string(resp.Body), 300),
		}
	}
	if allowAccepted && resp.Status == http.StatusAccepted {
		return map[string]any{"status": "accepted"}, nil
	}
	success := resp.Status == http.StatusOK
	if allowAccepted {
		success = resp.Status == http.StatusOK || resp.Status == http.StatusCreated
	}
	if !success {
		return nil, map[string]any{
			"error": "Microsoft Graph API error " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}
	}
	if len(resp.Body) == 0 {
		if allowAccepted {
			return map[string]any{"status": "success"}, nil
		}
		return nil, map[string]any{"error": "Microsoft Graph API error: empty response body"}
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return nil, map[string]any{"error": "Microsoft Graph API error: invalid JSON response"}
	}
	return data, nil
}

// batch7Lane9GraphGet performs one authenticated GET.
func batch7Lane9GraphGet(ctx context.Context, rt *Runtime, path string, query url.Values) (map[string]any, map[string]any) {
	endpoint := batch7Lane9GraphRoot(rt) + "/" + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	resp, err := marketingDoJSON(ctx, rt, http.MethodGet, endpoint, batch7Lane9GraphHeaders(rt), nil, 30*time.Second)
	if err != nil {
		return nil, batch7Lane9GraphTransport(ctx, err)
	}
	return batch7Lane9GraphDecode(resp, false)
}

// batch7Lane9GraphPost performs one authenticated JSON POST.
func batch7Lane9GraphPost(ctx context.Context, rt *Runtime, path string, payload any) (map[string]any, map[string]any) {
	resp, err := marketingDoJSON(ctx, rt, http.MethodPost,
		batch7Lane9GraphRoot(rt)+"/"+path, batch7Lane9GraphHeaders(rt), payload, 30*time.Second)
	if err != nil {
		return nil, batch7Lane9GraphTransport(ctx, err)
	}
	return batch7Lane9GraphDecode(resp, true)
}

// batch7Lane9GraphTransport renders a transport failure.
func batch7Lane9GraphTransport(ctx context.Context, err error) map[string]any {
	if isTimeout(ctx, err) {
		return map[string]any{"error": "Request to Microsoft Graph API timed out"}
	}
	return map[string]any{"error": "Microsoft Graph API request failed: " + err.Error()}
}

func batch7Lane9GraphListMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	folder := stringArg(input, "folder")
	if folder == "" {
		folder = "inbox"
	}
	query := url.Values{
		"$top":     []string{strconv.Itoa(clampInt(intArg(input, "max_results", 20), 1, 50))},
		"$select":  []string{"id,subject,from,receivedDateTime,isRead,hasAttachments,bodyPreview"},
		"$orderby": []string{"receivedDateTime desc"},
	}
	if boolArg(input, "filter_unread", false) {
		query.Set("$filter", "isRead eq false")
	}
	if search := stringArg(input, "search"); search != "" {
		query.Set("$search", `"`+search+`"`)
	}
	data, failure := batch7Lane9GraphGet(ctx, rt, "me/mailFolders/"+url.PathEscape(folder)+"/messages", query)
	if failure != nil {
		return failure, nil
	}
	messages := make([]any, 0)
	for _, raw := range asSlice(data["value"]) {
		message := asMap(raw)
		fromAddr := asMap(asMap(message["from"])["emailAddress"])
		messages = append(messages, map[string]any{
			"id":               stringOr(message, "id", ""),
			"subject":          stringOr(message, "subject", ""),
			"from_name":        stringOr(fromAddr, "name", ""),
			"from_email":       stringOr(fromAddr, "address", ""),
			"receivedDateTime": stringOr(message, "receivedDateTime", ""),
			"isRead":           asBool(message["isRead"]),
			"hasAttachments":   asBool(message["hasAttachments"]),
			"bodyPreview":      stringOr(message, "bodyPreview", ""),
		})
	}
	return map[string]any{"folder": folder, "messages": messages}, nil
}

func batch7Lane9GraphGetMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	messageID := stringArg(input, "message_id")
	if messageID == "" {
		return map[string]any{"error": "message_id is required"}, nil
	}
	data, failure := batch7Lane9GraphGet(ctx, rt, "me/messages/"+url.PathEscape(messageID), nil)
	if failure != nil {
		return failure, nil
	}
	fromAddr := asMap(asMap(data["from"])["emailAddress"])
	toList := make([]any, 0)
	for _, raw := range asSlice(data["toRecipients"]) {
		address := asMap(asMap(raw)["emailAddress"])
		toList = append(toList, map[string]any{
			"name":  stringOr(address, "name", ""),
			"email": stringOr(address, "address", ""),
		})
	}
	body := asMap(data["body"])
	categories := asSlice(data["categories"])
	if categories == nil {
		categories = []any{}
	}
	importance := stringOr(data, "importance", "normal")
	return map[string]any{
		"id":               stringOr(data, "id", ""),
		"subject":          stringOr(data, "subject", ""),
		"from_name":        stringOr(fromAddr, "name", ""),
		"from_email":       stringOr(fromAddr, "address", ""),
		"to":               toList,
		"body":             stringOr(body, "content", ""),
		"bodyContentType":  stringOr(body, "contentType", ""),
		"receivedDateTime": stringOr(data, "receivedDateTime", ""),
		"hasAttachments":   asBool(data["hasAttachments"]),
		"importance":       importance,
		"categories":       categories,
		"isRead":           asBool(data["isRead"]),
	}, nil
}

// batch7Lane9GraphRecipients splits a comma-separated address list into Graph
// recipient objects.
func batch7Lane9GraphRecipients(value string) []any {
	recipients := make([]any, 0)
	for _, address := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(address); trimmed != "" {
			recipients = append(recipients, map[string]any{
				"emailAddress": map[string]any{"address": trimmed},
			})
		}
	}
	return recipients
}

func batch7Lane9GraphSendMail(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	to, subject := stringArg(input, "to"), stringArg(input, "subject")
	if to == "" || subject == "" {
		return map[string]any{"error": "to and subject are required"}, nil
	}
	bodyType := stringArg(input, "body_type")
	if bodyType == "" {
		bodyType = "Text"
	}
	message := map[string]any{
		"subject":      subject,
		"body":         map[string]any{"contentType": bodyType, "content": stringArg(input, "body")},
		"toRecipients": batch7Lane9GraphRecipients(to),
	}
	if cc := stringArg(input, "cc"); cc != "" {
		message["ccRecipients"] = batch7Lane9GraphRecipients(cc)
	}
	payload := map[string]any{
		"message":         message,
		"saveToSentItems": boolArg(input, "save_to_sent", true),
	}
	if _, failure := batch7Lane9GraphPost(ctx, rt, "me/sendMail", payload); failure != nil {
		return failure, nil
	}
	return map[string]any{"status": "sent", "to": to, "subject": subject}, nil
}

func batch7Lane9GraphListTeams(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	data, failure := batch7Lane9GraphGet(ctx, rt, "me/joinedTeams", nil)
	if failure != nil {
		return failure, nil
	}
	teams := make([]any, 0)
	for _, raw := range asSlice(data["value"]) {
		team := asMap(raw)
		teams = append(teams, map[string]any{
			"id":          stringOr(team, "id", ""),
			"displayName": stringOr(team, "displayName", ""),
			"description": stringOr(team, "description", ""),
		})
	}
	return map[string]any{"teams": teams}, nil
}

func batch7Lane9GraphListChannels(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	teamID := stringArg(input, "team_id")
	if teamID == "" {
		return map[string]any{"error": "team_id is required"}, nil
	}
	data, failure := batch7Lane9GraphGet(ctx, rt, "teams/"+url.PathEscape(teamID)+"/channels", nil)
	if failure != nil {
		return failure, nil
	}
	channels := make([]any, 0)
	for _, raw := range asSlice(data["value"]) {
		channel := asMap(raw)
		channels = append(channels, map[string]any{
			"id":             stringOr(channel, "id", ""),
			"displayName":    stringOr(channel, "displayName", ""),
			"description":    stringOr(channel, "description", ""),
			"membershipType": stringOr(channel, "membershipType", ""),
		})
	}
	return map[string]any{"team_id": teamID, "channels": channels}, nil
}

func batch7Lane9GraphSendChannelMessage(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	teamID, channelID, text := stringArg(input, "team_id"), stringArg(input, "channel_id"), stringArg(input, "message")
	if teamID == "" || channelID == "" || text == "" {
		return map[string]any{"error": "team_id, channel_id, and message are required"}, nil
	}
	contentType := stringArg(input, "content_type")
	if contentType == "" {
		contentType = "text"
	}
	payload := map[string]any{"body": map[string]any{"contentType": contentType, "content": text}}
	data, failure := batch7Lane9GraphPost(ctx, rt,
		"teams/"+url.PathEscape(teamID)+"/channels/"+url.PathEscape(channelID)+"/messages", payload)
	if failure != nil {
		return failure, nil
	}
	return map[string]any{
		"status":     "sent",
		"messageId":  stringOr(data, "id", ""),
		"team_id":    teamID,
		"channel_id": channelID,
	}, nil
}

func batch7Lane9GraphChannelMessages(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	teamID, channelID := stringArg(input, "team_id"), stringArg(input, "channel_id")
	if teamID == "" || channelID == "" {
		return map[string]any{"error": "team_id and channel_id are required"}, nil
	}
	query := url.Values{
		"$top": []string{strconv.Itoa(clampInt(intArg(input, "max_results", 20), 1, 50))},
	}
	data, failure := batch7Lane9GraphGet(ctx, rt,
		"teams/"+url.PathEscape(teamID)+"/channels/"+url.PathEscape(channelID)+"/messages", query)
	if failure != nil {
		return failure, nil
	}
	messages := make([]any, 0)
	for _, raw := range asSlice(data["value"]) {
		message := asMap(raw)
		fromUser := asMap(asMap(message["from"])["user"])
		body := asMap(message["body"])
		messages = append(messages, map[string]any{
			"id":              stringOr(message, "id", ""),
			"from_name":       stringOr(fromUser, "displayName", ""),
			"body":            stringOr(body, "content", ""),
			"contentType":     stringOr(body, "contentType", ""),
			"createdDateTime": stringOr(message, "createdDateTime", ""),
		})
	}
	return map[string]any{"team_id": teamID, "channel_id": channelID, "messages": messages}, nil
}

func batch7Lane9GraphSearchFiles(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	query := stringArg(input, "query")
	if query == "" {
		return map[string]any{"error": "query is required"}, nil
	}
	top := url.Values{
		"$top": []string{strconv.Itoa(clampInt(intArg(input, "max_results", 20), 1, 50))},
	}
	data, failure := batch7Lane9GraphGet(ctx, rt,
		"me/drive/root/search(q='"+url.PathEscape(query)+"')", top)
	if failure != nil {
		return failure, nil
	}
	files := make([]any, 0)
	for _, raw := range asSlice(data["value"]) {
		item := asMap(raw)
		files = append(files, map[string]any{
			"id":                   stringOr(item, "id", ""),
			"name":                 stringOr(item, "name", ""),
			"size":                 marketingIntOr(item, "size", 0),
			"lastModifiedDateTime": stringOr(item, "lastModifiedDateTime", ""),
			"webUrl":               stringOr(item, "webUrl", ""),
			"mimeType":             stringOr(asMap(item["file"]), "mimeType", ""),
			"path":                 stringOr(asMap(item["parentReference"]), "path", ""),
		})
	}
	return map[string]any{"query": query, "files": files}, nil
}

func batch7Lane9GraphListFiles(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	folderPath := stringArg(input, "folder_path")
	top := url.Values{
		"$top": []string{strconv.Itoa(clampInt(intArg(input, "max_results", 50), 1, 200))},
	}
	endpoint := "me/drive/root/children"
	if folderPath != "" {
		endpoint = "me/drive/root:/" + folderPath + ":/children"
	}
	data, failure := batch7Lane9GraphGet(ctx, rt, endpoint, top)
	if failure != nil {
		return failure, nil
	}
	items := make([]any, 0)
	for _, raw := range asSlice(data["value"]) {
		item := asMap(raw)
		itemType := "file"
		if item["folder"] != nil {
			itemType = "folder"
		}
		items = append(items, map[string]any{
			"id":                   stringOr(item, "id", ""),
			"name":                 stringOr(item, "name", ""),
			"size":                 marketingIntOr(item, "size", 0),
			"type":                 itemType,
			"lastModifiedDateTime": stringOr(item, "lastModifiedDateTime", ""),
			"webUrl":               stringOr(item, "webUrl", ""),
		})
	}
	path := folderPath
	if path == "" {
		path = "/"
	}
	return map[string]any{"path": path, "items": items}, nil
}

// batch7Lane9GraphIsText reports whether a MIME type is decoded as text.
func batch7Lane9GraphIsText(contentType string) bool {
	if strings.HasPrefix(contentType, "text/") {
		return true
	}
	switch contentType {
	case "application/json", "application/xml", "application/javascript":
		return true
	default:
		return false
	}
}

func batch7Lane9GraphDownloadFile(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	itemID, filePath := stringArg(input, "item_id"), stringArg(input, "file_path")
	metaPath := ""
	if itemID != "" {
		metaPath = "me/drive/items/" + url.PathEscape(itemID)
	} else if filePath != "" {
		metaPath = "me/drive/root:/" + filePath
	} else {
		return map[string]any{"error": "Provide one of: item_id or file_path"}, nil
	}
	meta, failure := batch7Lane9GraphGet(ctx, rt, metaPath, nil)
	if failure != nil {
		return failure, nil
	}
	downloadURL := stringOr(meta, "@microsoft.graph.downloadUrl", "")
	if downloadURL == "" {
		if itemID != "" {
			downloadURL = batch7Lane9GraphRoot(rt) + "/me/drive/items/" + url.PathEscape(itemID) + "/content"
		} else {
			downloadURL = batch7Lane9GraphRoot(rt) + "/me/drive/root:/" + filePath + ":/content"
		}
	}
	resp, err := batch7Lane9Send(ctx, rt, http.MethodGet, downloadURL,
		map[string]string{"Authorization": "Bearer " + batch7Lane9GraphToken(rt)}, nil, 60*time.Second)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "File download timed out"}, nil
		}
		return map[string]any{"error": "Download failed: " + err.Error()}, nil
	}
	if resp.Status != http.StatusOK {
		return map[string]any{"error": "Download failed with status " + strconv.Itoa(resp.Status)}, nil
	}
	contentType := stringOr(asMap(meta["file"]), "mimeType", "application/octet-stream")
	content, encoding := "", "base64"
	if batch7Lane9GraphIsText(contentType) {
		content, encoding = string(resp.Body), "text"
	} else {
		content = base64.StdEncoding.EncodeToString(resp.Body)
	}
	return map[string]any{
		"name":         stringOr(meta, "name", ""),
		"size":         marketingIntOr(meta, "size", 0),
		"content_type": contentType,
		"content":      content,
		"encoding":     encoding,
	}, nil
}

func batch7Lane9GraphUploadFile(ctx context.Context, rt *Runtime, input map[string]any) (any, error) {
	if batch7Lane9GraphToken(rt) == "" {
		return batch7Lane9GraphAuthError(), nil
	}
	filePath, content := stringArg(input, "file_path"), stringArg(input, "content")
	if filePath == "" || content == "" {
		return map[string]any{"error": "file_path and content are required"}, nil
	}
	contentType := stringArg(input, "content_type")
	if contentType == "" {
		contentType = "text/plain"
	}
	endpoint := batch7Lane9GraphRoot(rt) + "/me/drive/root:/" + filePath + ":/content"
	resp, err := batch7Lane9Send(ctx, rt, http.MethodPut, endpoint, map[string]string{
		"Authorization": "Bearer " + batch7Lane9GraphToken(rt),
		"Content-Type":  contentType,
	}, []byte(content), 60*time.Second)
	if err != nil {
		if isTimeout(ctx, err) {
			return map[string]any{"error": "File upload timed out"}, nil
		}
		return map[string]any{"error": "Upload failed: " + err.Error()}, nil
	}
	if resp.Status != http.StatusOK && resp.Status != http.StatusCreated {
		return map[string]any{
			"error": "Upload failed with status " + strconv.Itoa(resp.Status) + ": " + truncateText(string(resp.Body), 500),
		}, nil
	}
	data, ok := jsonObject(resp.Body)
	if !ok {
		return map[string]any{"error": "Upload failed: invalid JSON response"}, nil
	}
	return map[string]any{
		"status": "uploaded",
		"name":   stringOr(data, "name", ""),
		"id":     stringOr(data, "id", ""),
		"size":   marketingIntOr(data, "size", 0),
		"webUrl": stringOr(data, "webUrl", ""),
	}, nil
}
