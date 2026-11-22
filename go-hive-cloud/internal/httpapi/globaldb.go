package httpapi

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/shark2202/go_hive/go-hive-cloud/internal/store"
)

// POST /v1/global-db/sql — free-form SQL (DDL + DML + SELECT), gated.
func (s *Server) handleGlobalSQL(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	var body struct {
		SQL    string `json:"sql"`
		RowCap *int   `json:"row_cap"`
	}
	if err := decodeJSON(w, r, maxGlobalDBBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.SQL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sql is required"})
		return
	}
	rowCap := 0
	if body.RowCap != nil {
		rowCap = *body.RowCap
	}
	result, err := team.Run(r.Context(), body.SQL, rowCap)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if result.Kind != store.KindRead {
		op := "write"
		switch result.Kind {
		case store.KindInsert:
			op = "insert"
		case store.KindUpdate:
			op = "update"
		case store.KindDelete:
			op = "delete"
		case store.KindSchema:
			op = "schema"
		}
		if result.Table != "" {
			if err := team.RecordChange(r.Context(), result.Table, "", op); err != nil {
				s.internalError(w, r, err)
				return
			}
		}
		if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "sql", result.Table,
			fmt.Sprintf("op=%s row_count=%d", op, result.RowCount)); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /v1/global-db/query — read-only SQL.
func (s *Server) handleGlobalQuery(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	var body struct {
		SQL    string `json:"sql"`
		RowCap *int   `json:"row_cap"`
	}
	if err := decodeJSON(w, r, maxGlobalDBBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.SQL) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "sql is required"})
		return
	}
	rowCap := 0
	if body.RowCap != nil {
		rowCap = *body.RowCap
	}
	result, err := team.RunReadOnly(r.Context(), body.SQL, rowCap)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /v1/global-db/upsert.
func (s *Server) handleGlobalUpsert(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	var body struct {
		Table        string         `json:"table"`
		Row          map[string]any `json:"row"`
		SourceColony string         `json:"source_colony"`
		Mode         string         `json:"mode"`
	}
	if err := decodeJSON(w, r, maxGlobalDBBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.Table) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "table is required"})
		return
	}
	if len(body.Row) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "row must be a non-empty object"})
		return
	}
	result, err := team.Upsert(r.Context(), body.Table, body.Row, body.Mode)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	op := "insert"
	if result.Updated > 0 {
		op = "update"
	}
	if err := team.RecordChangeJSON(r.Context(), body.Table, result.PK, op); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "upsert", body.Table,
		fmt.Sprintf("mode=%s inserted=%d updated=%d source_colony=%s", body.Mode, result.Inserted, result.Updated, body.SourceColony)); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /v1/global-db/import.
func (s *Server) handleGlobalImport(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	var body struct {
		Table        string           `json:"table"`
		Rows         []map[string]any `json:"rows"`
		SourceColony string           `json:"source_colony"`
	}
	if err := decodeJSON(w, r, maxGlobalDBBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(body.Table) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "table is required"})
		return
	}
	if len(body.Rows) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "rows must be a non-empty array"})
		return
	}
	result, err := team.ImportRows(r.Context(), body.Table, body.Rows)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if err := team.RecordChange(r.Context(), body.Table, "", "import"); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "import", body.Table,
		fmt.Sprintf("rows=%d source_colony=%s", result.Inserted, body.SourceColony)); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /v1/global-db/tables.
func (s *Server) handleGlobalTables(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	tables, err := team.ListTables(r.Context())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

// GET /v1/global-db/changes?since=<cursor>.
func (s *Server) handleGlobalChanges(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	result, err := team.Changes(r.Context(), r.URL.Query().Get("since"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /v1/global-db/tables/{table}/rows.
func (s *Server) handleGlobalRows(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	page, err := team.ListRows(r.Context(), r.PathValue("table"), r.URL.Query())
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// PATCH /v1/global-db/tables/{table}/rows.
func (s *Server) handleGlobalPatchRows(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	var body struct {
		PK      map[string]any `json:"pk"`
		Updates map[string]any `json:"updates"`
	}
	if err := decodeJSON(w, r, maxGlobalDBBody, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	if len(body.PK) == 0 || len(body.Updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "pk and updates must be objects"})
		return
	}
	table := r.PathValue("table")
	updated, err := team.UpdateRow(r.Context(), table, body.PK, body.Updates)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	if updated == 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "row not found"})
		return
	}
	if err := team.RecordChangeJSON(r.Context(), table, body.PK, "update"); err != nil {
		s.internalError(w, r, err)
		return
	}
	if err := team.Audit(r.Context(), reqAuth.UserID, reqAuth.TeamID, "update", table,
		fmt.Sprintf("updated=%d", updated)); err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
}
