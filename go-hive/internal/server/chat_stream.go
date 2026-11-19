package server

import (
	"context"
	"errors"
	"sync"

	"github.com/aden-hive/hive-go/internal/llm"
)

// chatStreamingCompleter bridges a streaming provider to the Completer the
// agent loop calls. The loop has one LLM seam (AComplete); when the configured
// provider can stream, this adapter runs the streaming call and forwards each
// text increment to the turn's emitter as a client_output_delta frame.
//
// It only streams the conversation call: compaction and other auxiliary calls
// share the same completer but a different system prompt, and must keep their
// text out of the chat transcript.
type chatStreamingCompleter struct {
	completer llm.Completer
	streamer  llm.StreamCompleter

	mu sync.Mutex
	// streaming is true between beginTurn and endTurn.
	streaming bool
	// disabled latches after the provider reports ErrStreamingUnsupported, so
	// later turns skip the failed streaming attempt.
	disabled bool
	// systemPrompt identifies the conversation call inside a turn.
	systemPrompt string
	// sink publishes one delta; nil means the session has no live event bus.
	sink func(content, snapshot string, iteration int)
	// iteration is set by LoopObserver.IterationStarted, which the loop calls
	// immediately before each LLM request.
	iteration int
	// textIteration is the last iteration whose text was already streamed, so
	// the loop's end-of-call AssistantText does not publish it a second time.
	textIteration int
}

// newChatStreamingCompleter returns nil when the provider cannot stream, which
// keeps the non-streaming path byte-for-byte unchanged.
func newChatStreamingCompleter(completer llm.Completer) *chatStreamingCompleter {
	if completer == nil {
		return nil
	}
	streamer, ok := completer.(llm.StreamCompleter)
	if !ok {
		return nil
	}
	return &chatStreamingCompleter{completer: completer, streamer: streamer}
}

// ModelName forwards the provider's model so the loop keeps resolving the same
// model name it did before the adapter existed.
func (c *chatStreamingCompleter) ModelName() string {
	if namer, ok := c.completer.(llm.ModelNamer); ok {
		return namer.ModelName()
	}
	return ""
}

// beginTurn arms streaming for one turn. systemPrompt is the conversation
// system prompt; only a request carrying it is treated as the model call that
// belongs to the chat transcript.
func (c *chatStreamingCompleter) beginTurn(systemPrompt string, sink func(content, snapshot string, iteration int)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.streaming = true
	c.systemPrompt = systemPrompt
	c.sink = sink
	c.iteration = 0
	c.textIteration = 0
	c.mu.Unlock()
}

func (c *chatStreamingCompleter) endTurn() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.streaming = false
	c.sink = nil
	c.iteration = 0
	c.mu.Unlock()
}

// noteIteration records the loop iteration the next LLM call belongs to.
func (c *chatStreamingCompleter) noteIteration(iteration int) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.iteration = iteration
	c.mu.Unlock()
}

// textStreamed reports whether the given iteration's assistant text already
// reached the bus as deltas.
func (c *chatStreamingCompleter) textStreamed(iteration int) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.textIteration != 0 && c.textIteration == iteration
}

// AComplete streams the conversation call and falls back to the non-streaming
// completion when the provider (or an auxiliary call) cannot stream.
func (c *chatStreamingCompleter) AComplete(ctx context.Context, req llm.CompletionRequest) (llm.Completion, error) {
	if c == nil || c.completer == nil {
		return llm.Completion{}, errors.New("server: chat streaming completer is not initialised")
	}

	c.mu.Lock()
	streamThisCall := !c.disabled && c.streaming && c.sink != nil && req.System == c.systemPrompt
	sink := c.sink
	c.mu.Unlock()
	if !streamThisCall {
		return c.completer.AComplete(ctx, req)
	}

	streamed := false
	completion, err := c.streamer.ACompleteStream(ctx, req, func(event llm.StreamEvent) {
		delta, ok := event.(llm.TextDeltaEvent)
		if !ok || delta.Content == "" {
			return
		}
		c.mu.Lock()
		iteration := c.iteration
		c.textIteration = iteration
		c.mu.Unlock()
		streamed = true
		sink(delta.Content, delta.Snapshot, iteration)
	})
	if err == nil {
		return completion, nil
	}
	if errors.Is(err, llm.ErrStreamingUnsupported) && !streamed {
		// The transport told us up front that it cannot stream; run this call
		// and every later one through the ordinary completion path.
		c.mu.Lock()
		c.disabled = true
		c.mu.Unlock()
		return c.completer.AComplete(ctx, req)
	}
	return llm.Completion{}, err
}
