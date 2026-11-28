package tasks

// Parity: framework/tasks/models.py::TaskStatus

import (
	"fmt"
	"time"
)

type TaskStatus string

const (
	TaskStatusPending    TaskStatus = "pending"
	TaskStatusInProgress TaskStatus = "in_progress"
	TaskStatusCompleted  TaskStatus = "completed"
	TaskStatusAbandoned  TaskStatus = "abandoned"
	TaskStatusArchived   TaskStatus = "archived"
)

// ValidTaskStatus reports whether value is a TaskStatus member. Python's task
// tools take the status as a StrEnum, so anything else raises
// "'<value>' is not a valid TaskStatus" before the store is touched.
func ValidTaskStatus(value string) bool {
	switch TaskStatus(value) {
	case TaskStatusPending, TaskStatusInProgress, TaskStatusCompleted, TaskStatusAbandoned, TaskStatusArchived:
		return true
	}
	return false
}

// InvalidTaskStatusMessage mirrors the ValueError Python's StrEnum raises
// before the store is touched: "'bogus' is not a valid TaskStatus".
func InvalidTaskStatusMessage(value any) string {
	if text, ok := value.(string); ok {
		return fmt.Sprintf("'%s' is not a valid TaskStatus", text)
	}
	return fmt.Sprintf("%v is not a valid TaskStatus", value)
}

const IdleTaskThresholdSeconds float64 = 30 * 60

type TaskRecord struct {
	ID          int            `json:"id"`
	Subject     string         `json:"subject"`
	Description string         `json:"description"`
	ActiveForm  *string        `json:"active_form"`
	Owner       *string        `json:"owner"`
	Status      TaskStatus     `json:"status"`
	Blocks      []int          `json:"blocks"`
	BlockedBy   []int          `json:"blocked_by"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   float64        `json:"created_at"`
	UpdatedAt   float64        `json:"updated_at"`
}

func NewTaskRecord(id int, subject string) *TaskRecord {
	now := NowSeconds()
	return &TaskRecord{
		ID:        id,
		Subject:   subject,
		Status:    TaskStatusPending,
		Blocks:    []int{},
		BlockedBy: []int{},
		Metadata:  map[string]any{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func (r *TaskRecord) Clone() *TaskRecord {
	if r == nil {
		return nil
	}
	c := *r
	c.Blocks = append([]int(nil), r.Blocks...)
	c.BlockedBy = append([]int(nil), r.BlockedBy...)
	c.Metadata = CloneMap(r.Metadata)
	return &c
}

type TaskListMeta struct {
	Role           string  `json:"role"`
	CreatorAgentID *string `json:"creator_agent_id"`
	Goal           *string `json:"goal"`
	CreatedAt      float64 `json:"created_at"`
	SchemaVersion  int     `json:"schema_version"`
}

func NewTaskListMeta() *TaskListMeta {
	return &TaskListMeta{
		Role:          "session",
		CreatedAt:     NowSeconds(),
		SchemaVersion: 1,
	}
}

type TaskListDocument struct {
	Meta          *TaskListMeta `json:"meta"`
	Highwatermark int           `json:"highwatermark"`
	Tasks         []*TaskRecord `json:"tasks"`
}

type ClaimResult struct {
	Kind      string
	Record    *TaskRecord
	By        string
	BlockedBy []int
}

func ClaimOk(record *TaskRecord) ClaimResult {
	return ClaimResult{Kind: "ok", Record: record}
}

func ClaimNotFound() ClaimResult { return ClaimResult{Kind: "not_found"} }

func ClaimAlreadyOwned(by string) ClaimResult {
	return ClaimResult{Kind: "already_owned", By: by}
}

func ClaimAlreadyCompleted() ClaimResult {
	return ClaimResult{Kind: "already_completed"}
}

func ClaimBlocked(by []int) ClaimResult {
	return ClaimResult{Kind: "blocked", BlockedBy: append([]int(nil), by...)}
}

func IsTaskIdle(record *TaskRecord, now float64) bool {
	if record.Status != TaskStatusInProgress {
		return false
	}
	current := now
	if current <= 0 {
		current = NowSeconds()
	}
	return current-record.UpdatedAt > IdleTaskThresholdSeconds
}

func NowSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func CloneMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
