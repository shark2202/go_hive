package llm

import (
	"errors"
	"net/http"
)

// Stable error categories the desktop client can branch on without parsing a
// free-form failure string. Upstream only classifies payment and rate-limit
// failures today; everything else keeps a nil ErrorType and, when known, the
// raw upstream status.
//
// Parity: core/framework/llm/litellm.py::_classify_llm_error
const (
	LLMErrorTypePaymentRequired = "payment_required"
	LLMErrorTypeRateLimit       = "rate_limit"
)

// PaymentRequiredEmitter is the event-bus seam for the billing-gate event.
// *eventbus.EventBus satisfies it; the session runtime is expected to install
// one on the provider (and set PaymentRequiredStreamID/ExecutionID) so a
// classified 402 reaches the SSE feed as payment_required instead of only a
// generic execution failure. The variadic status mirrors
// EventBus.EmitPaymentRequired.
//
// Parity: core/framework/agent_loop/agent_loop.py (StreamErrorEvent
// error_type=="payment_required" -> event_bus.emit_payment_required)
type PaymentRequiredEmitter interface {
	EmitPaymentRequired(streamID string, executionID *string, message *string, upstreamStatus ...*int)
}

// ClassifyLLMError mirrors _classify_llm_error: map a provider failure to the
// coarse error_type plus the upstream HTTP status when one is known.
//
// Parity: core/framework/llm/litellm.py::_classify_llm_error
func ClassifyLLMError(err error) (*string, *int) {
	if err == nil {
		return nil, nil
	}
	status := 0
	var providerErr *ProviderError
	if errors.As(err, &providerErr) && providerErr.StatusCode != 0 {
		status = providerErr.StatusCode
	}
	if status == 0 {
		return nil, nil
	}
	if status == http.StatusPaymentRequired {
		errorType := LLMErrorTypePaymentRequired
		return &errorType, &status
	}
	if status == http.StatusTooManyRequests {
		errorType := LLMErrorTypeRateLimit
		return &errorType, &status
	}
	return nil, &status
}

// NewStreamErrorEvent builds the stream terminal error with the classification
// fields the SSE client reads. Recoverable mirrors upstream's transient-error
// set for HTTP failures: only gateway/server unavailability is retryable;
// exhausted 429s and billing failures are not.
//
// Parity: core/framework/llm/litellm.py::_classify_llm_error and
// ::LiteLLMProvider.stream (StreamErrorEvent construction)
func NewStreamErrorEvent(err error) StreamErrorEvent {
	event := StreamErrorEvent{}
	if err == nil {
		return event
	}
	event.Error = err.Error()
	event.ErrorType, event.UpstreamStatus = ClassifyLLMError(err)
	if event.UpstreamStatus != nil {
		switch *event.UpstreamStatus {
		case http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			event.Recoverable = true
		}
	} else if isStreamTransientError(err) {
		// Transport-level failures (connection reset, timeout) carry no HTTP
		// status but upstream still classifies them recoverable.
		event.Recoverable = true
	}
	return event
}

// emitStreamError publishes one classified terminal stream error and, for a
// 402, forwards it to the installed payment-required sink so the dedicated SSE
// event fires before the turn fails.
//
// Parity: core/framework/agent_loop/agent_loop.py::_run_turn_loop
// (StreamErrorEvent handler -> emit_payment_required)
func (p *Provider) emitStreamError(emit func(StreamEvent), err error) {
	event := NewStreamErrorEvent(err)
	emit(event)
	p.emitPaymentRequired(event)
}

func (p *Provider) emitPaymentRequired(event StreamErrorEvent) {
	if p == nil || p.PaymentRequiredSink == nil {
		return
	}
	if event.ErrorType == nil || *event.ErrorType != LLMErrorTypePaymentRequired {
		return
	}
	message := event.Error
	p.PaymentRequiredSink.EmitPaymentRequired(
		p.PaymentRequiredStreamID,
		p.PaymentRequiredExecutionID,
		&message,
		event.UpstreamStatus,
	)
}
