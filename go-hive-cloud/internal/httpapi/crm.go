package httpapi

import (
	"fmt"
	"net/http"
)

// CRM grid handlers. The open-source clients expose these ten routes as thin
// pass-throughs to the closed hive-backend CRM service; this private
// deployment implements the evidence-backed generic grid subset documented in
// docs/m4-notes.md. Schema, claim and release stay unregistered until their
// request/response shapes can be recovered.

// POST /v1/crm/grid/{entity}/query
func (s *Server) handleCRMQuery(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	body, err := decodeCRMObject(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	page, err := team.CRMQueryRecords(r.Context(), r.PathValue("entity"), body)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// POST /v1/crm/grid/{entity}/group-counts
func (s *Server) handleCRMGroupCounts(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	body, err := decodeCRMObject(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	counts, err := team.CRMGroupCounts(r.Context(), r.PathValue("entity"), body)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, counts)
}

// GET /v1/crm/grid/{entity}/{id}
func (s *Server) handleCRMGet(w http.ResponseWriter, r *http.Request) {
	team, _, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	page, err := team.CRMGetRecord(r.Context(), r.PathValue("entity"), r.PathValue("id"))
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// POST /v1/crm/grid/{entity}  body: {fields, identities?, because?}
func (s *Server) handleCRMCreate(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	body, err := decodeCRMObject(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	page, err := team.CRMCreateRecord(r.Context(), r.PathValue("entity"), body, reqAuth.UserID, reqAuth.TeamID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// PATCH /v1/crm/grid/{entity}/{id}  body: {fields?, identities?, because?}
func (s *Server) handleCRMPatch(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	body, err := decodeCRMObject(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	page, err := team.CRMUpdateRecord(r.Context(), r.PathValue("entity"), r.PathValue("id"), body, reqAuth.UserID, reqAuth.TeamID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// DELETE /v1/crm/grid/{entity}/{id} — soft-delete (archive).
//
// The client proxy treats the DELETE body as optional and forwards a malformed
// body as null (routes_colony_workers.py::_crm_proxy), so this handler tolerates
// an absent/unparseable body.
func (s *Server) handleCRMDelete(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	var body map[string]any
	if r.Body != nil && r.ContentLength != 0 {
		if decoded, err := decodeCRMObject(w, r); err == nil {
			body = decoded
		}
	}
	deleted, err := team.CRMDeleteRecord(r.Context(), r.PathValue("entity"), r.PathValue("id"), body, reqAuth.UserID, reqAuth.TeamID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

// POST /v1/crm/grid/{entity}/{id}/stage  body: {value, because?}
func (s *Server) handleCRMStage(w http.ResponseWriter, r *http.Request) {
	team, reqAuth, ok := s.teamFrom(r)
	if !ok {
		s.internalError(w, r, fmt.Errorf("team unavailable"))
		return
	}
	body, err := decodeCRMObject(w, r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
		return
	}
	page, err := team.CRMSetStage(r.Context(), r.PathValue("entity"), r.PathValue("id"), body, reqAuth.UserID, reqAuth.TeamID)
	if err != nil {
		s.writeStoreError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func decodeCRMObject(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := decodeJSON(w, r, maxGlobalDBBody, &body); err != nil {
		return nil, err
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, nil
}
