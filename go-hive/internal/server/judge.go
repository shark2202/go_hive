package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/hivehome"
)

const JudgeTimeout = 20 * time.Second

type JudgeProtocol func(slot map[string]any, question string, state map[string]any, criteria map[string]string) (float64, error)

// Parity: framework/server/judge.py::PROTOCOLS
var JudgeProtocols = map[string]JudgeProtocol{"typesafe": TypesafeJudge}

// Parity: framework/server/judge.py::slot
func JudgeSlot(home hivehome.Home) map[string]any {
	slot, _ := HiveConfig(home)["judge"].(map[string]any)
	if slot == nil {
		return nil
	}
	if !pyTruthy(slot["api_base"]) || !pyTruthy(slot["api_key"]) {
		return nil
	}
	return slot
}

// Parity: framework/server/judge.py::ask
func AskJudge(home hivehome.Home, question string, state map[string]any, criteria map[string]string, protocols map[string]JudgeProtocol) (float64, bool) {
	slot := JudgeSlot(home)
	if slot == nil {
		return 0, false
	}
	registry := protocols
	if len(registry) == 0 {
		registry = JudgeProtocols
	}
	name := pyStrOr(slot["protocol"], "")
	proto, ok := registry[name]
	if !ok || proto == nil {
		names := make([]string, 0, len(registry))
		for key := range registry {
			names = append(names, key)
		}
		log.Printf("judge: protocol %q is not supported (use one of %v)", pyStrOr(slot["protocol"], ""), names)
		return 0, false
	}
	p, err := proto(slot, question, state, criteria)
	if err != nil {
		log.Printf("judge: unavailable (%v)", err)
		return 0, false
	}
	return p, true
}

// Parity: framework/server/judge.py::typesafe
func TypesafeJudge(slot map[string]any, question string, state map[string]any, criteria map[string]string) (float64, error) {
	answer := map[string]any{"type": "noul", "instructions": question}
	if len(criteria) > 0 {
		answer["criteria"] = criteria
	}
	payload := map[string]any{
		"state":     state,
		"model":     slot["model"],
		"questions": map[string]any{"answer": answer},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), JudgeTimeout)
	defer cancel()
	url := strings.TrimRight(pyStrOr(slot["api_base"], ""), "/") + "/systemone"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pyStr(slot["api_key"]))
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("judge endpoint returned HTTP %d", resp.StatusCode)
	}

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return 0, err
	}
	answers, ok := decoded["answers"].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("judge response has no answers object")
	}
	first, ok := answers["answer"].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("judge response has no answer object")
	}
	p, ok := toFloat(first["noul"])
	if !ok {
		return 0, fmt.Errorf("judge answer has no numeric noul")
	}
	return p, nil
}

func pyStrOr(value any, fallback string) string {
	if pyTruthy(value) {
		return pyStr(value)
	}
	return pyStr(fallback)
}
