package agentloop

// Parity: framework/agent_loop/reminders.py LoopActivity.
type LoopActivity string

const (
	LoopActivityExecuting    LoopActivity = "executing"
	LoopActivityAwaitingUser LoopActivity = "awaiting_user"
	LoopActivityInterrupted  LoopActivity = "interrupted"
)

// Parity: framework/agent_loop/reminders.py ParkReason.
type ParkReason string

const (
	ParkReasonAskUser          ParkReason = "ask_user"
	ParkReasonCredentialForm   ParkReason = "credential_form"
	ParkReasonColonySuggestion ParkReason = "colony_suggestion"
	ParkReasonTurnDone         ParkReason = "turn_done"
	ParkReasonAwaitingQueen    ParkReason = "awaiting_queen"
	ParkReasonUserStopped      ParkReason = "user_stopped"
	ParkReasonColdInterrupted  ParkReason = "cold_interrupted"

	ParkReasonLLMError       ParkReason = "llm_error"
	ParkReasonEmptyResponses ParkReason = "empty_responses"
	ParkReasonDoomLoop       ParkReason = "doom_loop"

	ParkReasonUnknown ParkReason = "unknown"
)

// Parity: framework/agent_loop/reminders.py ParkReason members.
var AllParkReasons = []ParkReason{
	ParkReasonAskUser,
	ParkReasonCredentialForm,
	ParkReasonColonySuggestion,
	ParkReasonTurnDone,
	ParkReasonAwaitingQueen,
	ParkReasonUserStopped,
	ParkReasonColdInterrupted,
	ParkReasonLLMError,
	ParkReasonEmptyResponses,
	ParkReasonDoomLoop,
	ParkReasonUnknown,
}

// Parity: framework/agent_loop/reminders.py ParkReason.is_external_wait.
func (p ParkReason) IsExternalWait() bool {
	switch p {
	case ParkReasonAskUser,
		ParkReasonCredentialForm,
		ParkReasonColonySuggestion,
		ParkReasonAwaitingQueen:
		return true
	}
	return false
}

// Parity: framework/agent_loop/reminders.py ParkReason.is_silent_park.
func (p ParkReason) IsSilentPark() bool {
	switch p {
	case ParkReasonTurnDone,
		ParkReasonUserStopped,
		ParkReasonColdInterrupted:
		return true
	}
	return false
}

// Parity: framework/agent_loop/reminders.py ParkReason.is_broken.
func (p ParkReason) IsBroken() bool {
	switch p {
	case ParkReasonLLMError,
		ParkReasonEmptyResponses,
		ParkReasonDoomLoop:
		return true
	}
	return false
}

// Parity: framework/agent_loop/reminders.py ParkReason.activity.
func (p ParkReason) Activity() LoopActivity {
	switch p {
	case ParkReasonAskUser,
		ParkReasonCredentialForm,
		ParkReasonColonySuggestion,
		ParkReasonTurnDone,
		ParkReasonAwaitingQueen,
		ParkReasonColdInterrupted:
		return LoopActivityAwaitingUser
	}
	return LoopActivityInterrupted
}

// Parity: framework/agent_loop/reminders.py LoopSignals.
type LoopSignals struct {
	IdleSeconds    float64
	AwaitingInput  bool
	ParkReason     *ParkReason
	Activity       *LoopActivity
	UserStopped    bool
	StreamActive   bool
	FirstEventSeen bool
	StallReason    string
	StallElapsed   float64
}
