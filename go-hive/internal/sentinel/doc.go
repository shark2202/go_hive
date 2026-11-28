// Package sentinel is the Go port of the colony-queen autopilot: when the
// queen pauses before its goal is done, decide whether to nudge it onward,
// escalate a genuine blocker to the human, or do nothing.
//
// The port mirrors core/framework/sentinel module-for-module:
//
//	manager.go            delivery, inbound routing, resume, token index
//	classifier.go         nudge-vs-escalate one-shot LLM judgement
//	escalation_source.go  the temporal decision engine (agentloop source)
//	listeners.go          Telegram long-poll, Slack Socket Mode, Hive SSE
//	socket_dialer.go      stdlib RFC 6455 client for Socket Mode
//	notifier.go           Telegram / Slack / Hive Inbox senders
//	runtime_identity.go   stable per-runtime id and cloud/local kind
//	store.go              notifications config + escalation records on disk
//	token.go              HMAC correlation tokens and their lifecycle
//
// Time and messaging are injectable (Clock, Notifier) so thresholds,
// deduplication, token rotation and persistence are testable without a
// network or a wall clock. Hosts wire the process-global manager with
// SetSentinelManager and register NewEscalationSource with the reminder hub.
package sentinel
