package cli

// Queen mail tools: list_queens / message_queen, the two tools a queen writes
// to another queen with. Registered per session from the shared queen-mail
// service the HTTP routes use, so a letter sent here is delivered (and answered)
// by the same runtime as one sent through /api/queen-mail/letters.
//
// Parity: framework/tools/queen_mail_tools.py::register_queen_mail_tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/llm"
	"github.com/shark2202/go_hive/go-hive/internal/queenmail"
	hiveserver "github.com/shark2202/go_hive/go-hive/internal/server"
)

const (
	// ListQueensToolName names the queens this queen can write to.
	ListQueensToolName = "list_queens"
	// MessageQueenToolName sends one letter to another queen.
	MessageQueenToolName = "message_queen"
)

// queenMailToolBinding is the queen-mail slice of one session's catalogue: the
// shared service (also behind the routes) and the caller a letter is
// attributed to.
type queenMailToolBinding struct {
	mail      *queenmail.Mail
	sessionID string
	queenID   string
}

// enableQueenMail registers list_queens/message_queen on this session's
// catalogue when queen_mail is on and the session's queen takes part. It is
// called only for queen chat sessions: a colony worker's catalogue never
// enables them, matching upstream, where the tools are the queens' alone.
func (c *sessionToolCatalogue) enableQueenMail(mail *queenmail.Mail, sessionID, queenName string) {
	if c == nil || c.queenMail != nil {
		return
	}
	config := queenmail.DefaultConfig()
	if mail != nil {
		config = mail.Config()
	} else if c.home.Root != "" {
		// A server whose queen mail isn't running still advertises the tools
		// when configuration.json turns the feature on; each call then says so
		// instead of the queen silently losing the capability.
		config = queenmail.ConfigFromHive(hiveserver.HiveConfig(c.home))
	}
	queenID := strings.TrimSpace(queenName)
	// A session whose queen is picked later (no queen_name yet) is checked
	// when she writes; an unbound session is refused there too.
	if !config.Enabled || (queenID != "" && !config.Includes(queenID)) {
		return
	}
	binding := &queenMailToolBinding{mail: mail, sessionID: sessionID, queenID: queenID}
	defs := []llm.ToolDef{
		{
			Name:        ListQueensToolName,
			Description: "The other queens you can write to with message_queen: each one's id, name and title.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:        MessageQueenToolName,
			Description: messageQueenDescription(mail, queenID),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"queen_id": map[string]any{"type": "string", "description": "Her id, from list_queens."},
					"message": map[string]any{
						"type":        "string",
						"description": "What to tell her. She can't see your conversation, so say what she needs to know.",
					},
				},
				"required": []any{"queen_id", "message"},
			},
		},
	}
	added := 0
	for _, def := range defs {
		if _, taken := c.builtinNames[def.Name]; taken {
			continue
		}
		c.defs = append(c.defs, def)
		added++
	}
	if added > 0 {
		c.queenMail = binding
	}
}

// execute runs one queen-mail tool call; the second return reports whether the
// name belonged to this table.
func (b *queenMailToolBinding) execute(name string, input map[string]any) (string, bool) {
	switch name {
	case ListQueensToolName:
		if b == nil || b.mail == nil {
			return queenMailJSON(map[string]any{"error": "Queen mail isn't running in this server"}), true
		}
		return queenMailJSON(map[string]any{"queens": b.mail.Correspondents(b.queenID)}), true
	case MessageQueenToolName:
		if b == nil || b.mail == nil {
			return queenMailJSON(map[string]any{"error": "Queen mail isn't running in this server"}), true
		}
		queenID, _ := input["queen_id"].(string)
		message, _ := input["message"].(string)
		sent, err := b.mail.Send(b.queenID, b.sessionID, queenID, message, b.mail.CauseFor(b.sessionID))
		if err != nil {
			return queenMailJSON(map[string]any{"error": err.Error()}), true
		}
		return queenMailJSON(sent), true
	}
	return "", false
}

// messageQueenDescription is the tool's description, with the roster the queen
// can write to at session start appended (list_queens has it as it is now).
//
// Parity: framework/tools/queen_mail_tools.py::_roster
func messageQueenDescription(mail *queenmail.Mail, queenID string) string {
	description := "Write to another queen. It returns at once; her answer arrives later as a message from her, " +
		"\"[Name(queen_id)]: ...\", and each time you write she goes on with the same conversation with you. " +
		"When another queen writes to you, just answer: your reply goes back to her without this tool. " +
		"Returns {sent, to, depth}, or {error} (no such queen, today's limit reached, or the chain of letters " +
		"that led here is too long)."
	if mail == nil || queenID == "" {
		return description
	}
	queens := mail.Correspondents(queenID)
	if len(queens) == 0 {
		return description
	}
	parts := make([]string, 0, len(queens))
	for _, queen := range queens {
		id, _ := queen["id"].(string)
		name, _ := queen["name"].(string)
		title, _ := queen["title"].(string)
		if title != "" {
			parts = append(parts, fmt.Sprintf("%s (%s, %s)", name, id, title))
		} else {
			parts = append(parts, fmt.Sprintf("%s (%s)", name, id))
		}
	}
	return description + " Queens you can write to: " + strings.Join(parts, "; ") + "."
}

func queenMailJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		// The values are plain maps/strings; a marshal failure is a bug, not a
		// tool outcome, but the queen still gets an explicit error.
		return `{"error":"message_queen result could not be encoded"}`
	}
	return string(data)
}
