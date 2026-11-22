package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/shark2202/go_hive/go-hive/internal/globaldb"
)

type TableColumn struct {
	Name    string
	Type    string
	NotNull bool
	PK      int
}

type GlobalUpsert struct {
	Table string
	Row   map[string]any
	Mode  string
}

type InsertPlan struct {
	Status      int
	Body        map[string]any
	Upsert      *GlobalUpsert
	AttemptedPK map[string]any
}

// Parity: framework/server/routes_colony_workers.py::_normalize_linkedin
func NormalizeLinkedIn(url string) string {
	u := strings.ToLower(strings.TrimSpace(url))
	if rest, ok := strings.CutPrefix(u, "http://"); ok {
		u = rest
	} else if rest, ok := strings.CutPrefix(u, "https://"); ok {
		u = rest
	}
	u = strings.TrimPrefix(u, "www.")
	if cut := strings.IndexAny(u, "?#"); cut >= 0 {
		u = u[:cut]
	}
	return strings.TrimRight(u, "/")
}

// Parity: framework/server/routes_colony_workers.py::_derive_lead_id
func DeriveLeadID(row map[string]any) (id string, ok bool) {
	if li, isString := row["linkedin_url"].(string); isString {
		if slug := NormalizeLinkedIn(li); slug != "" {
			return slug, true
		}
	}
	if email, isString := row["email"].(string); isString {
		if trimmed := strings.TrimSpace(email); trimmed != "" {
			return strings.ToLower(trimmed), true
		}
	}
	return "", false
}

// Parity: framework/server/routes_colony_workers.py::handle_global_insert_row
func PlanInsertRow(table string, row map[string]any, cols []TableColumn, pk []string, mintID func() string) InsertPlan {
	copied := make(map[string]any, len(row)+1)
	for k, v := range row {
		copied[k] = v
	}

	if len(pk) == 1 && !rowTruthy(copied[pk[0]]) {
		key := pk[0]
		if table == "leads" && key == "lead_id" {
			derived, ok := DeriveLeadID(copied)
			if !ok {
				return InsertPlan{
					Status: 400,
					Body:   map[string]any{"error": "Add an email or LinkedIn URL to identify the lead."},
				}
			}
			copied[key] = derived
		} else {
			colType := ""
			for _, c := range cols {
				if c.Name == key {
					colType = c.Type
					break
				}
			}
			if !strings.Contains(strings.ToLower(colType), "int") {
				if mintID == nil {
					mintID = newHexID
				}
				copied[key] = mintID()
			}
		}
	}

	attemptedPK := make(map[string]any, len(pk))
	for _, c := range pk {
		attemptedPK[c] = copied[c]
	}
	return InsertPlan{
		Status:      200,
		Upsert:      &GlobalUpsert{Table: table, Row: copied, Mode: "insert"},
		AttemptedPK: attemptedPK,
	}
}

// Parity: framework/server/routes_colony_workers.py::handle_global_insert_row
func MapInsertResult(plan InsertPlan, result map[string]any, err error) (int, map[string]any) {
	if err != nil {
		var conflict *globaldb.GlobalDbError
		if errors.As(err, &conflict) && conflict.Status == 409 {
			return 409, map[string]any{
				"error":   "conflict",
				"message": conflict.Error(),
				"pk":      plan.AttemptedPK,
			}
		}
		return GlobalDBErrorResponse(err)
	}

	var inserted any = 0
	if result != nil {
		if v, ok := result["inserted"]; ok {
			inserted = v
		}
	}
	return 200, map[string]any{"inserted": inserted, "pk": plan.AttemptedPK}
}

// Parity: framework/server/routes_colony_workers.py::_global_db_error_response
func GlobalDBErrorResponse(err error) (int, map[string]any) {
	var signedOut *globaldb.NotSignedInError
	if errors.As(err, &signedOut) {
		return 401, map[string]any{"error": signedOut.Error(), "signed_out": true}
	}
	var dbErr *globaldb.GlobalDbError
	if errors.As(err, &dbErr) {
		status := dbErr.Status
		if status == 0 {
			status = 400
		}
		return status, map[string]any{"error": dbErr.Error()}
	}
	return 502, map[string]any{"error": "global_db_proxy_error"}
}

// Parity: framework/server/routes_colony_workers.py::handle_global_changes
func ChangesSinceParam(since string) *string {
	if since == "" {
		return nil
	}
	return &since
}

func rowTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case float32:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

func newHexID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
