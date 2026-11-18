package server

// Parity: framework/server/boot_status.py

import (
	"sync"
	"time"
)

const MaxBootHistory = 32

type BootStageEntry struct {
	Stage  string  `json:"stage"`
	Detail string  `json:"detail"`
	At     float64 `json:"at"`
}

type BootState struct {
	Ready     bool             `json:"ready"`
	Stage     string           `json:"stage"`
	Detail    string           `json:"detail"`
	UpdatedAt float64          `json:"updated_at"`
	History   []BootStageEntry `json:"history"`
}

var (
	bootMu    sync.Mutex
	bootState = BootState{
		Ready:     false,
		Stage:     "Starting runtime",
		Detail:    "",
		UpdatedAt: unixSeconds(),
		History:   []BootStageEntry{},
	}
)

func unixSeconds() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func recordBootStage(stage, detail string, ready bool) {
	now := unixSeconds()
	bootMu.Lock()
	defer bootMu.Unlock()
	bootState.Ready = ready
	bootState.Stage = stage
	bootState.Detail = detail
	bootState.UpdatedAt = now
	bootState.History = append(bootState.History, BootStageEntry{
		Stage:  stage,
		Detail: detail,
		At:     now,
	})
	if len(bootState.History) > MaxBootHistory {
		trimmed := make([]BootStageEntry, MaxBootHistory)
		copy(trimmed, bootState.History[len(bootState.History)-MaxBootHistory:])
		bootState.History = trimmed
	}
}

func ReportBootStage(stage string, detail ...string) {
	value := ""
	if len(detail) > 0 {
		value = detail[0]
	}
	recordBootStage(stage, value, false)
}

func MarkBootReady(stage ...string) {
	value := "Ready"
	if len(stage) > 0 {
		value = stage[0]
	}
	recordBootStage(value, "", true)
}

func BootStatus() BootState {
	bootMu.Lock()
	defer bootMu.Unlock()
	history := make([]BootStageEntry, len(bootState.History))
	copy(history, bootState.History)
	return BootState{
		Ready:     bootState.Ready,
		Stage:     bootState.Stage,
		Detail:    bootState.Detail,
		UpdatedAt: bootState.UpdatedAt,
		History:   history,
	}
}
