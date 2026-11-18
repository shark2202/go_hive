package tasks

// Parity: framework/tasks/scoping.py

import (
	"os"
	"strings"
)

func SessionTaskListID(agentID, sessionID string) string {
	return "session:" + agentID + ":" + sessionID
}

func ColonyTaskListID(colonyID string) string {
	return "colony:" + colonyID
}

type ParsedTaskListID struct {
	Kind      string
	AgentID   string
	SessionID string
	ColonyID  string
	Value     string
}

func ParseTaskListID(taskListID string) ParsedTaskListID {
	if strings.HasPrefix(taskListID, "session:") {
		rest := taskListID[len("session:"):]
		agentID, sessionID, _ := strings.Cut(rest, ":")
		return ParsedTaskListID{Kind: "session", AgentID: agentID, SessionID: sessionID}
	}
	if strings.HasPrefix(taskListID, "colony:") {
		return ParsedTaskListID{Kind: "colony", ColonyID: taskListID[len("colony:"):]}
	}
	if strings.HasPrefix(taskListID, "unscoped:") {
		return ParsedTaskListID{Kind: "unscoped", AgentID: taskListID[len("unscoped:"):]}
	}
	return ParsedTaskListID{Kind: "raw", Value: taskListID}
}

type TaskContext struct {
	TaskListID  string
	AgentID     string
	RunID       string
	ExecutionID string
	StreamID    string
}

func ResolveTaskListID(ctx TaskContext) string {
	if override := os.Getenv("HIVE_TASK_LIST_ID"); override != "" {
		return override
	}
	if ctx.TaskListID != "" {
		return ctx.TaskListID
	}
	sessionID := ctx.RunID
	if sessionID == "" {
		sessionID = ctx.ExecutionID
	}
	if sessionID == "" {
		sessionID = ctx.StreamID
	}
	if ctx.AgentID != "" && sessionID != "" {
		return SessionTaskListID(ctx.AgentID, sessionID)
	}
	agent := ctx.AgentID
	if agent == "" {
		agent = "unknown"
	}
	return "unscoped:" + agent
}
