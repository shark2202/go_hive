package server

// Parity: framework/server/stall_check.py

import (
	"log"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aden-hive/hive-go/internal/agentloop"
	"github.com/aden-hive/hive-go/internal/eventbus"
	"github.com/aden-hive/hive-go/internal/hivehome"
)

// Parity: framework/server/stall_check.py::_QUESTION
const StallCheckQuestion = "This turn left undone work the assistant should have done in it: its reply announced or promised an action " +
	"(run a command, call a tool, draw or send an image, look something up), or the user asked for something to be " +
	"done or redone (including by pointing out what is wrong with work the assistant delivered) and the reply only " +
	"agreed or described what should change; and no tool call in this turn did it."

// Parity: framework/server/stall_check.py::_CRITERIA
var StallCheckCriteria = map[string]string{
	"true": "The reply talks about work it should be doing now (announces it, promises it, or agrees to a correction " +
		"and says what should change) and no tool call this turn does that work.",
	"false": "Nothing the user wants done now was left undone: the reply is conversation; answers a question; asks " +
		"something it needs to know first; declines or argues against the request, even while admitting a mistake or " +
		"offering an alternative; responds to the user disputing or explaining something rather than asking for new " +
		"work; or the tools called this turn did the work, even if the result is imperfect and the reply lists flaws " +
		"that remain.",
}

// Parity: framework/server/stall_check.py::REMINDER
const StallReminder = "<system-reminder>\n" +
	"Your last reply talked about work to do now (announced or promised it, or agreed to a change the user asked " +
	"for), but you ended the turn without calling any tool to do it, so nothing was done. If it should be done, " +
	"call the tools now. If not, carry on. Don't mention this reminder.\n" +
	"</system-reminder>"

// Parity: framework/server/stall_check.py::judge_slot
func StallJudgeSlot(home hivehome.Home) map[string]any {
	if !StallCheckEnabled(home) {
		return nil
	}
	return JudgeSlot(home)
}

type stallTurn struct {
	user      string
	reasoning string
	reply     string
	tools     []string
	judged    bool
	previous  map[string]any
}

type StallCheckSubscription struct {
	bus           *eventbus.EventBus
	subscriptions []string
	waiters       sync.WaitGroup
}

func (s *StallCheckSubscription) Subscriptions() []string {
	return append([]string(nil), s.subscriptions...)
}

func (s *StallCheckSubscription) Wait() {
	s.waiters.Wait()
}

func (s *StallCheckSubscription) Close() {
	for _, id := range s.subscriptions {
		s.bus.Unsubscribe(id)
	}
	s.waiters.Wait()
}

// Parity: framework/server/stall_check.py::subscribe_stall_check
func SubscribeStallCheck(bus *eventbus.EventBus, home hivehome.Home, queenNode func() QueenNode, judges map[string]JudgeProtocol) *StallCheckSubscription {
	turn := &stallTurn{}
	var mu sync.Mutex
	s := &StallCheckSubscription{bus: bus}

	check := func(user, reasoning, reply string, tools []string, previous map[string]any) {
		slot := StallJudgeSlot(home)
		if slot == nil {
			return
		}
		if tools == nil {
			tools = []string{}
		}
		state := map[string]any{}
		if len(previous) > 0 {
			state["previous_turn"] = previous
		}
		state["user_message"] = tailRunes(user, 2000)
		state["assistant_reasoning_tail"] = tailRunes(reasoning, 1500)
		state["assistant_reply"] = tailRunes(reply, 1500)
		state["tools_called_this_turn"] = tools

		p, ok := AskJudge(home, StallCheckQuestion, state, StallCheckCriteria, judges)
		if !ok {
			return
		}
		threshold := 0.5
		if raw, present := slot["threshold"]; present {
			f, coercible := toFloat(raw)
			if !coercible {
				return
			}
			threshold = f
		}
		if p < threshold {
			log.Printf("stall check: p=%.2f < %.2f, turn is fine", p, threshold)
			return
		}
		node := queenNode()
		if node == nil {
			return
		}
		log.Printf("stall check: p=%.2f >= %.2f, reminding the queen to do the work she talked about", p, threshold)
		if err := node.InjectEvent(StallReminder, false); err != nil {
			log.Printf("stall check: remind the queen: %v", err)
		}
	}

	onEvent := func(event *eventbus.AgentEvent) {
		if event.StreamID != "queen" {
			return
		}
		mu.Lock()
		switch event.Type {
		case eventbus.EventClientInputReceived:
			if strings.TrimFunc(turn.reply, pySpace) != "" {
				turn.previous = map[string]any{
					"assistant_reply": tailRunes(turn.reply, 600),
					"tools_called":    append([]string{}, turn.tools...),
				}
			}
			turn.user = pyStrOr(event.Data["content"], "")
			turn.reasoning = ""
			turn.reply = ""
			turn.tools = nil
			turn.judged = false
		case eventbus.EventClientReasoning:
			turn.reasoning = pyStrOr(event.Data["reasoning"], turn.reasoning)
		case eventbus.EventClientOutputDelta:
			turn.reply = pyStrOr(event.Data["snapshot"], turn.reply)
		case eventbus.EventToolCallStarted:
			turn.tools = append(turn.tools, pyStrOr(event.Data["tool_name"], ""))
			turn.reasoning = ""
			turn.reply = ""
		case eventbus.EventClientInputRequested:
			reason, _ := event.Data["park_reason"].(string)
			if reason != "turn_done" {
				break
			}
			if turn.judged || turn.user == "" || strings.TrimFunc(turn.reply, pySpace) == "" || agentloop.IsVisibleError(turn.reply) {
				break
			}
			turn.judged = true
			user, reasoning, reply := turn.user, turn.reasoning, turn.reply
			tools := append([]string{}, turn.tools...)
			previous := turn.previous
			s.waiters.Add(1)
			go func() {
				defer s.waiters.Done()
				check(user, reasoning, reply, tools, previous)
			}()
		}
		mu.Unlock()
	}

	s.subscriptions = []string{bus.Subscribe([]eventbus.EventType{
		eventbus.EventClientInputReceived,
		eventbus.EventClientReasoning,
		eventbus.EventClientOutputDelta,
		eventbus.EventToolCallStarted,
		eventbus.EventClientInputRequested,
	}, onEvent)}
	return s
}

func tailRunes(s string, n int) string {
	count := utf8.RuneCountInString(s)
	if count <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[count-n:])
}
