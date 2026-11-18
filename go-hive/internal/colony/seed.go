package colony

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aden-hive/hive-go/internal/eventbus"
)

const DefaultSeedWait = 30 * time.Second

const DefaultSeedPoll = 200 * time.Millisecond

type QueenNode interface {
	InjectEvent(ctx context.Context, content string, isClientInput bool) error
}

type SeedSession interface {
	SessionID() string
	EventBus() *eventbus.EventBus
	LiveQueenNode() QueenNode
}

type SeedOptions struct {
	Wait  time.Duration
	Poll  time.Duration
	Warnf func(format string, args ...any)
}

func (o SeedOptions) resolved() SeedOptions {
	if o.Wait <= 0 {
		o.Wait = DefaultSeedWait
	}
	if o.Poll <= 0 {
		o.Poll = DefaultSeedPoll
	}
	if o.Warnf == nil {
		o.Warnf = func(string, ...any) {}
	}
	return o
}

func SeedColonyQueen(ctx context.Context, session SeedSession, colonyID string, userGoal *string, opts SeedOptions) bool {
	opts = opts.resolved()

	goal := ""
	if userGoal != nil {
		goal = strings.TrimSpace(*userGoal)
	}

	deadline := time.Now().Add(opts.Wait)
	var node QueenNode
	for {
		node = session.LiveQueenNode()
		if node != nil {
			break
		}
		if !time.Now().Before(deadline) {
			opts.Warnf("colony '%s' queen never came up; not seeded", colonyID)
			return false
		}
		select {
		case <-ctx.Done():
			opts.Warnf("colony '%s' queen never came up; not seeded", colonyID)
			return false
		case <-time.After(opts.Poll):
		}
	}

	if goal != "" {
		session.EventBus().Publish(&eventbus.AgentEvent{
			Type:        eventbus.EventClientInputReceived,
			StreamID:    "queen",
			NodeID:      eventbus.StrPtr("queen"),
			ExecutionID: eventbus.StrPtr(session.SessionID()),
			Data:        map[string]any{"content": goal, "image_count": 0},
		})
		if err := node.InjectEvent(ctx, goal, true); err != nil {
			opts.Warnf("failed to seed colony '%s': %v", colonyID, err)
			return false
		}
		return true
	}

	note := fmt.Sprintf("[Colony '%s' is ready and you are its queen. Carry on with the work agreed in the conversation above.]", colonyID)
	if err := node.InjectEvent(ctx, note, false); err != nil {
		opts.Warnf("failed to seed colony '%s': %v", colonyID, err)
		return false
	}
	return true
}
