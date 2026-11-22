package skills

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/shark2202/go_hive/go-hive/internal/atomicfs"
)

const InstallRequestsFileName = "skill_installs.json"

const (
	InstallStatusPending  = "pending"
	InstallStatusApproved = "approved"
	InstallStatusDenied   = "denied"
)

type InstallRequestFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type InstallRequest struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Body        string               `json:"body"`
	Files       []InstallRequestFile `json:"files"`
	RequestedAt string               `json:"requested_at"`
	Status      string               `json:"status"`
	DecidedAt   string               `json:"decided_at,omitempty"`
}

type RequestNotFound struct {
	ID string
}

func (e *RequestNotFound) Error() string {
	return "no skill install request " + e.ID
}

type RequestDecided struct {
	ID     string
	Status string
}

func (e *RequestDecided) Error() string {
	return fmt.Sprintf("skill install request %s was already %s", e.ID, e.Status)
}

func AddRequest(sessionDir string, draft *SkillDraft) (*InstallRequest, error) {
	existing := loadInstallRequests(sessionDir)
	requests := make([]*InstallRequest, 0, len(existing)+1)
	for _, request := range existing {
		if request.Status == InstallStatusPending && request.Name == draft.Name {
			continue
		}
		requests = append(requests, request)
	}

	files := make([]InstallRequestFile, 0, len(draft.Files))
	for _, file := range draft.Files {
		files = append(files, InstallRequestFile{Path: file.RelPath, Content: file.Content})
	}
	request := &InstallRequest{
		ID:          newInstallRequestID(),
		Name:        draft.Name,
		Description: draft.Description,
		Body:        draft.Body,
		Files:       files,
		RequestedAt: UTCNow().Format(time.RFC3339Nano),
		Status:      InstallStatusPending,
	}
	requests = append(requests, request)
	if err := saveInstallRequests(sessionDir, requests); err != nil {
		return nil, err
	}
	return request, nil
}

func PendingRequests(sessionDir string) []*InstallRequest {
	out := []*InstallRequest{}
	for _, request := range loadInstallRequests(sessionDir) {
		if request.Status == InstallStatusPending {
			out = append(out, request)
		}
	}
	return out
}

func PendingRequest(sessionDir, requestID string) (*InstallRequest, error) {
	for _, request := range loadInstallRequests(sessionDir) {
		if request.ID == requestID {
			if request.Status != InstallStatusPending {
				return nil, &RequestDecided{ID: requestID, Status: request.Status}
			}
			return request, nil
		}
	}
	return nil, &RequestNotFound{ID: requestID}
}

func MarkDecided(sessionDir, requestID, decision string) error {
	requests := loadInstallRequests(sessionDir)
	for _, request := range requests {
		if request.ID == requestID {
			request.Status = decision
			request.DecidedAt = UTCNow().Format(time.RFC3339Nano)
		}
	}
	return saveInstallRequests(sessionDir, requests)
}

func DraftOf(request *InstallRequest) (*SkillDraft, error) {
	rawFiles := make([]map[string]any, 0, len(request.Files))
	for _, file := range request.Files {
		rawFiles = append(rawFiles, map[string]any{"path": file.Path, "content": file.Content})
	}
	draft, errMsg := BuildSkillDraft(request.Name, request.Description, request.Body, rawFiles)
	if draft == nil {
		return nil, fmt.Errorf("skill install request %s is no longer a valid skill: %s", request.ID, errMsg)
	}
	return draft, nil
}

func installRequestsPath(sessionDir string) string {
	return filepath.Join(sessionDir, InstallRequestsFileName)
}

func loadInstallRequests(sessionDir string) []*InstallRequest {
	data, err := os.ReadFile(installRequestsPath(sessionDir))
	if err != nil {
		return []*InstallRequest{}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return []*InstallRequest{}
	}
	list, ok := decoded.([]any)
	if !ok {
		return []*InstallRequest{}
	}
	out := make([]*InstallRequest, 0, len(list))
	for _, entry := range list {
		request, ok := installRequestFromMap(entry)
		if ok {
			out = append(out, request)
		}
	}
	return out
}

func installRequestFromMap(entry any) (*InstallRequest, bool) {
	m, ok := entry.(map[string]any)
	if !ok {
		return nil, false
	}
	id, ok := m["id"].(string)
	if !ok {
		return nil, false
	}
	request := &InstallRequest{
		ID:          id,
		Name:        stringField(m, "name"),
		Description: stringField(m, "description"),
		Body:        stringField(m, "body"),
		RequestedAt: stringField(m, "requested_at"),
		Status:      stringField(m, "status"),
		DecidedAt:   stringField(m, "decided_at"),
		Files:       []InstallRequestFile{},
	}
	if rawFiles, ok := m["files"].([]any); ok {
		for _, rawFile := range rawFiles {
			fileMap, ok := rawFile.(map[string]any)
			if !ok {
				continue
			}
			request.Files = append(request.Files, InstallRequestFile{
				Path:    stringField(fileMap, "path"),
				Content: stringField(fileMap, "content"),
			})
		}
	}
	return request, true
}

func stringField(m map[string]any, key string) string {
	if value, ok := m[key].(string); ok {
		return value
	}
	return ""
}

func saveInstallRequests(sessionDir string, requests []*InstallRequest) error {
	if requests == nil {
		requests = []*InstallRequest{}
	}
	data, err := marshalInstallRequests(requests)
	if err != nil {
		return err
	}
	return atomicfs.WriteFile(installRequestsPath(sessionDir), data, 0o644)
}

func marshalInstallRequests(requests []*InstallRequest) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(requests); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

func newInstallRequestID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(raw[:])
}
