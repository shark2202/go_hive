package integrations

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// batch7Lane9GraphRawFake serves the Graph module and hands the raw request
// body to the handler (OneDrive upload sends text, not JSON).
func batch7Lane9GraphRawFake(t *testing.T, handler func(http.ResponseWriter, *http.Request, []byte)) *Registry {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		handler(w, r, raw)
	}))
	t.Cleanup(server.Close)
	return newBatch7Lane9Registry(
		WithBaseURL("microsoft_graph_tool", server.URL),
		WithHTTPClient(server.Client()),
		WithGetenv(func(string) string { return "" }),
		WithCredential("microsoft_graph_tool", "test-token"),
	)
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOutlookListMessages::test_successful_list
func TestBatch7Lane9GraphListMessages(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodGet || r.URL.Path != "/me/mailFolders/inbox/messages" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			query := r.URL.Query()
			if query.Get("$top") != "20" || query.Get("$orderby") != "receivedDateTime desc" {
				t.Fatalf("query = %v", query)
			}
			if query.Get("$select") != "id,subject,from,receivedDateTime,isRead,hasAttachments,bodyPreview" {
				t.Fatalf("$select = %q", query.Get("$select"))
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
				t.Fatalf("Authorization = %q", got)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{
					"id": "msg-1", "subject": "Hello",
					"from":             map[string]any{"emailAddress": map[string]any{"name": "Alice", "address": "alice@example.com"}},
					"receivedDateTime": "2024-01-01T00:00:00Z", "isRead": false,
					"hasAttachments": false, "bodyPreview": "Hi there",
				}},
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "outlook_list_messages"})
	if result["folder"] != "inbox" {
		t.Fatalf("result = %v", result)
	}
	message := batch4BodyMap(t, batch4BodySlice(t, result["messages"])[0])
	if message["subject"] != "Hello" || message["from_email"] != "alice@example.com" || message["isRead"] != false {
		t.Fatalf("message = %v", message)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOutlookListMessages::test_missing_token
func TestBatch7Lane9GraphMissingToken(t *testing.T) {
	registry := batch7Lane9EnvRegistry(map[string]string{})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "outlook_list_messages"})
	if got := batch4ErrorText(t, result); got != "MICROSOFT_GRAPH_ACCESS_TOKEN not set" {
		t.Fatalf("error = %q", got)
	}
	if result["help"] == nil {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream does not assert filter/search query construction; the
// Graph OData parameters are pinned here.
func TestBatch7Lane9GraphListMessagesFilters(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			query := r.URL.Query()
			if query.Get("$filter") != "isRead eq false" {
				t.Fatalf("$filter = %q", query.Get("$filter"))
			}
			if query.Get("$search") != `"quarterly report"` {
				t.Fatalf("$search = %q", query.Get("$search"))
			}
			if query.Get("$top") != "50" {
				t.Fatalf("$top = %q", query.Get("$top"))
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "outlook_list_messages", "filter_unread": true,
		"search": "quarterly report", "max_results": 100,
	})
	if result["folder"] != "inbox" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOutlookGetMessage::test_successful_get
func TestBatch7Lane9GraphGetMessage(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/me/messages/msg-1" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"id": "msg-1", "subject": "Test Email",
				"from":             map[string]any{"emailAddress": map[string]any{"name": "Bob", "address": "bob@example.com"}},
				"toRecipients":     []any{map[string]any{"emailAddress": map[string]any{"name": "Alice", "address": "alice@example.com"}}},
				"body":             map[string]any{"content": "<p>Hello</p>", "contentType": "html"},
				"receivedDateTime": "2024-01-01T00:00:00Z", "hasAttachments": false,
				"importance": "high", "categories": []any{"work"}, "isRead": true,
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "outlook_get_message", "message_id": "msg-1",
	})
	if result["subject"] != "Test Email" || result["from_email"] != "bob@example.com" {
		t.Fatalf("result = %v", result)
	}
	if result["body"] != "<p>Hello</p>" || result["bodyContentType"] != "html" || result["importance"] != "high" {
		t.Fatalf("result = %v", result)
	}
	to := batch4BodySlice(t, result["to"])
	if first := batch4BodyMap(t, to[0]); first["email"] != "alice@example.com" {
		t.Fatalf("to = %v", to)
	}
	if categories := batch4BodySlice(t, result["categories"]); len(categories) != 1 {
		t.Fatalf("categories = %v", categories)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOutlookGetMessage::test_missing_message_id
func TestBatch7Lane9GraphGetMessageValidation(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "outlook_get_message"})
	if got := batch4ErrorText(t, result); got != "message_id is required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOutlookSendMail::test_successful_send
func TestBatch7Lane9GraphSendMail(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/me/sendMail" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			message := batch4BodyMap(t, body["message"])
			if message["subject"] != "Test" {
				t.Fatalf("message = %v", message)
			}
			content := batch4BodyMap(t, message["body"])
			if content["contentType"] != "Text" || content["content"] != "Hello" {
				t.Fatalf("body = %v", content)
			}
			recipients := batch4BodySlice(t, message["toRecipients"])
			if address := batch4BodyMap(t, batch4BodyMap(t, recipients[0])["emailAddress"]); address["address"] != "alice@example.com" {
				t.Fatalf("toRecipients = %v", recipients)
			}
			if body["saveToSentItems"] != true {
				t.Fatalf("saveToSentItems = %v", body["saveToSentItems"])
			}
			w.WriteHeader(http.StatusAccepted)
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "outlook_send_mail", "to": "alice@example.com", "subject": "Test", "body": "Hello",
	})
	if result["status"] != "sent" || result["to"] != "alice@example.com" {
		t.Fatalf("result = %v", result)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOutlookSendMail::test_missing_fields
func TestBatch7Lane9GraphSendMailValidation(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "outlook_send_mail", "body": "test"})
	if got := batch4ErrorText(t, result); got != "to and subject are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestTeamsListTeams::test_successful_list
func TestBatch7Lane9GraphListTeams(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/me/joinedTeams" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{"id": "team-1", "displayName": "Engineering", "description": "Dev team"}},
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "teams_list_teams"})
	teams := batch4BodySlice(t, result["teams"])
	if first := batch4BodyMap(t, teams[0]); first["displayName"] != "Engineering" {
		t.Fatalf("teams = %v", teams)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestTeamsListChannels::test_successful_list
func TestBatch7Lane9GraphListChannels(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/teams/team-1/channels" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{
					"id": "ch-1", "displayName": "General", "description": "General channel", "membershipType": "standard",
				}},
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "teams_list_channels", "team_id": "team-1"})
	if result["team_id"] != "team-1" {
		t.Fatalf("result = %v", result)
	}
	channels := batch4BodySlice(t, result["channels"])
	if first := batch4BodyMap(t, channels[0]); first["displayName"] != "General" || first["membershipType"] != "standard" {
		t.Fatalf("channels = %v", channels)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestTeamsSendChannelMessage::test_successful_send
func TestBatch7Lane9GraphSendChannelMessage(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.Method != http.MethodPost || r.URL.Path != "/teams/team-1/channels/ch-1/messages" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			content := batch4BodyMap(t, body["body"])
			if content["contentType"] != "text" || content["content"] != "Hello team!" {
				t.Fatalf("body = %v", content)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{"id": "msg-123"})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "teams_send_channel_message", "team_id": "team-1", "channel_id": "ch-1", "message": "Hello team!",
	})
	if result["status"] != "sent" || result["messageId"] != "msg-123" {
		t.Fatalf("result = %v", result)
	}
}

// Parity-exempt: upstream has no channel-read test; the $top query and message
// shaping are pinned here.
func TestBatch7Lane9GraphChannelMessages(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/teams/team-1/channels/ch-1/messages" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if r.URL.Query().Get("$top") != "20" {
				t.Fatalf("$top = %q", r.URL.Query().Get("$top"))
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{
					"id": "m1", "from": map[string]any{"user": map[string]any{"displayName": "Alice"}},
					"body":            map[string]any{"content": "hi", "contentType": "text"},
					"createdDateTime": "2024-01-01T00:00:00Z",
				}},
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "teams_get_channel_messages", "team_id": "team-1", "channel_id": "ch-1",
	})
	message := batch4BodyMap(t, batch4BodySlice(t, result["messages"])[0])
	if message["from_name"] != "Alice" || message["body"] != "hi" {
		t.Fatalf("message = %v", message)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOneDriveSearchFiles::test_successful_search
func TestBatch7Lane9GraphSearchFiles(t *testing.T) {
	registry, recorder := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			if r.URL.Path != "/me/drive/root/search(q='report')" {
				t.Fatalf("path = %s", r.URL.Path)
			}
			if r.URL.Query().Get("$top") != "20" {
				t.Fatalf("$top = %q", r.URL.Query().Get("$top"))
			}
			batch4WriteJSON(w, http.StatusOK, map[string]any{
				"value": []any{map[string]any{
					"id": "file-1", "name": "report.pdf", "size": 1024,
					"lastModifiedDateTime": "2024-01-01T00:00:00Z",
					"webUrl":               "https://onedrive.live.com/report.pdf",
					"file":                 map[string]any{"mimeType": "application/pdf"},
					"parentReference":      map[string]any{"path": "/drive/root:/Documents"},
				}},
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "onedrive_search_files", "query": "report",
	})
	if result["query"] != "report" {
		t.Fatalf("result = %v", result)
	}
	file := batch4BodyMap(t, batch4BodySlice(t, result["files"])[0])
	if file["name"] != "report.pdf" || file["mimeType"] != "application/pdf" || file["path"] != "/drive/root:/Documents" {
		t.Fatalf("file = %v", file)
	}
	if recorder.count() != 1 {
		t.Fatalf("requests = %d", recorder.count())
	}
}

// Parity-exempt: upstream has no list-files test; both root and folder paths
// plus the folder/file discriminator are pinned here.
func TestBatch7Lane9GraphListFiles(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			switch r.URL.Path {
			case "/me/drive/root/children":
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"value": []any{
						map[string]any{"id": "f1", "name": "Documents", "folder": map[string]any{"childCount": 2}},
						map[string]any{"id": "f2", "name": "notes.txt", "size": 10},
					},
				})
			case "/me/drive/root:/Documents/Reports:/children":
				batch4WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
		})
	root := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "onedrive_list_files"})
	if root["path"] != "/" {
		t.Fatalf("root = %v", root)
	}
	items := batch4BodySlice(t, root["items"])
	if first := batch4BodyMap(t, items[0]); first["type"] != "folder" {
		t.Fatalf("items = %v", items)
	}
	if second := batch4BodyMap(t, items[1]); second["type"] != "file" {
		t.Fatalf("items = %v", items)
	}
	folder := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "onedrive_list_files", "folder_path": "Documents/Reports",
	})
	if folder["path"] != "Documents/Reports" {
		t.Fatalf("folder = %v", folder)
	}
}

// Parity-exempt: upstream has no download test; the provider downloadUrl, the
// text/binary split and the fallback content URL are pinned here.
func TestBatch7Lane9GraphDownloadFile(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			switch {
			case r.URL.Path == "/me/drive/items/text-1":
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"name": "notes.txt", "size": 5,
					"file":                         map[string]any{"mimeType": "text/plain"},
					"@microsoft.graph.downloadUrl": "http://" + r.Host + "/download/text",
				})
			case r.URL.Path == "/download/text":
				_, _ = w.Write([]byte("hello"))
			case r.URL.Path == "/me/drive/items/bin-1":
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"name": "doc.pdf", "size": 4,
					"file":                         map[string]any{"mimeType": "application/pdf"},
					"@microsoft.graph.downloadUrl": "http://" + r.Host + "/download/bin",
				})
			case r.URL.Path == "/download/bin":
				_, _ = w.Write([]byte{0x25, 0x50, 0x44, 0x46})
			case r.URL.Path == "/me/drive/items/fallback-1":
				batch4WriteJSON(w, http.StatusOK, map[string]any{
					"name": "fallback.txt", "size": 2,
					"file": map[string]any{"mimeType": "application/json"},
				})
			case r.URL.Path == "/me/drive/items/fallback-1/content":
				_, _ = w.Write([]byte(`{"ok":true}`))
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
		})
	text := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "onedrive_download_file", "item_id": "text-1",
	})
	if text["encoding"] != "text" || text["content"] != "hello" || text["content_type"] != "text/plain" {
		t.Fatalf("text = %v", text)
	}
	binary := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "onedrive_download_file", "item_id": "bin-1",
	})
	if binary["encoding"] != "base64" || binary["content"] != base64.StdEncoding.EncodeToString([]byte{0x25, 0x50, 0x44, 0x46}) {
		t.Fatalf("binary = %v", binary)
	}
	fallback := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "onedrive_download_file", "item_id": "fallback-1",
	})
	if fallback["encoding"] != "text" || fallback["content"] != `{"ok":true}` {
		t.Fatalf("fallback = %v", fallback)
	}
}

// Parity-exempt: upstream has no download validation test.
func TestBatch7Lane9GraphDownloadValidation(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "onedrive_download_file"})
	if got := batch4ErrorText(t, result); got != "Provide one of: item_id or file_path" {
		t.Fatalf("error = %q", got)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOneDriveUploadFile::test_successful_upload
func TestBatch7Lane9GraphUploadFile(t *testing.T) {
	registry := batch7Lane9GraphRawFake(t,
		func(w http.ResponseWriter, r *http.Request, raw []byte) {
			if r.Method != http.MethodPut || r.URL.Path != "/me/drive/root:/Documents/notes.txt:/content" {
				t.Fatalf("request = %s %s", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Content-Type"); got != "text/markdown" {
				t.Fatalf("Content-Type = %q", got)
			}
			if string(raw) != "Hello world" {
				t.Fatalf("body = %q", raw)
			}
			batch4WriteJSON(w, http.StatusCreated, map[string]any{
				"name": "notes.txt", "id": "file-2", "size": 11, "webUrl": "https://onedrive.live.com/notes.txt",
			})
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{
		"action": "onedrive_upload_file", "file_path": "Documents/notes.txt",
		"content": "Hello world", "content_type": "text/markdown",
	})
	if result["status"] != "uploaded" || result["name"] != "notes.txt" || result["id"] != "file-2" {
		t.Fatalf("result = %v", result)
	}
}

// Parity: tools/tests/tools/test_microsoft_graph_tool.py::TestOneDriveUploadFile::test_missing_fields
func TestBatch7Lane9GraphUploadValidation(t *testing.T) {
	registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
		func(w http.ResponseWriter, r *http.Request, body map[string]any) {
			t.Fatal("unexpected request")
		})
	result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "onedrive_upload_file"})
	if got := batch4ErrorText(t, result); got != "file_path and content are required" {
		t.Fatalf("error = %q", got)
	}
}

// Parity-exempt: provider statuses are not exercised upstream; this table pins
// the Graph error vocabulary.
func TestBatch7Lane9GraphStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusUnauthorized, "Unauthorized. Access token may be expired or invalid."},
		{http.StatusForbidden, "Forbidden. Missing required permission scope. Details: denied detail"},
		{http.StatusNotFound, "Microsoft Graph API error 404: missing"},
		{http.StatusTooManyRequests, "Microsoft Graph API error 429: slow down"},
	}
	bodies := map[int]string{
		http.StatusForbidden:       "denied detail",
		http.StatusNotFound:        "missing",
		http.StatusTooManyRequests: "slow down",
	}
	for _, testCase := range cases {
		registry, _ := batch7Lane9Fake(t, "microsoft_graph_tool", nil,
			func(w http.ResponseWriter, r *http.Request, body map[string]any) {
				w.WriteHeader(testCase.status)
				_, _ = w.Write([]byte(bodies[testCase.status]))
			})
		result := batch4Execute(t, registry, "microsoft_graph", map[string]any{"action": "teams_list_teams"})
		if got := batch4ErrorText(t, result); got != testCase.want {
			t.Fatalf("status %d: error = %q, want %q", testCase.status, got, testCase.want)
		}
	}
}
