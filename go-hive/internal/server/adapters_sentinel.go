package server

// Adapters projecting internal/sentinel onto the SentinelStore / SentinelNotifier
// seams used by RegisterSentinelRoutes. The shapes differ in three places:
//
//   - the notifications config/update structs are package-local copies,
//   - sentinel.Store reports a missing colony directory as ErrColonyNotFound,
//     while the routes compare against os.ErrNotExist,
//   - sentinel.HTTPNotifier.Send takes a context, a meta map and returns a
//     value (no error), while the seam returns (result, error).

import (
	"context"
	"errors"
	"os"

	"github.com/aden-hive/hive-go/internal/sentinel"
)

type sentinelStoreAdapter struct {
	store *sentinel.Store
}

func newSentinelStoreAdapter(hiveRoot string) SentinelStore {
	if hiveRoot == "" {
		return sentinelStoreAdapter{store: sentinel.DefaultStore()}
	}
	return sentinelStoreAdapter{store: sentinel.NewStore(hiveRoot)}
}

func (a sentinelStoreAdapter) LoadNotificationsConfig(colonyID string) SentinelNotificationsConfig {
	cfg := a.store.LoadNotificationsConfig(colonyID)
	return SentinelNotificationsConfig{
		SentinelEnabled:      cfg.SentinelEnabled,
		Channel:              cfg.Channel,
		Target:               cfg.Target,
		Allowlist:            cfg.Allowlist,
		Thread:               cfg.Thread,
		ClassifyAfterSeconds: cfg.ClassifyAfterSeconds,
	}
}

func (a sentinelStoreAdapter) UpdateNotificationsConfig(colonyID string, update SentinelNotificationsUpdate) error {
	err := a.store.UpdateNotificationsConfig(colonyID, sentinel.NotificationsUpdate{
		SentinelEnabled:      update.SentinelEnabled,
		Channel:              update.Channel,
		Target:               update.Target,
		Allowlist:            update.Allowlist,
		ClassifyAfterSeconds: update.ClassifyAfterSeconds,
	})
	if errors.Is(err, sentinel.ErrColonyNotFound) {
		return os.ErrNotExist
	}
	return err
}

type sentinelNotifierAdapter struct {
	notifier *sentinel.HTTPNotifier
}

func newSentinelNotifierAdapter() SentinelNotifier {
	return sentinelNotifierAdapter{notifier: sentinel.NewHTTPNotifier(nil)}
}

func (a sentinelNotifierAdapter) TelegramToken() string { return a.notifier.TelegramToken() }

func (a sentinelNotifierAdapter) SlackBotToken() string { return a.notifier.SlackBotToken() }

func (a sentinelNotifierAdapter) SlackAppToken() string { return a.notifier.SlackAppToken() }

func (a sentinelNotifierAdapter) Send(channel string, target map[string]any, text string, threadRef map[string]any) (SentinelSendResult, error) {
	result := a.notifier.Send(context.Background(), channel, target, text, threadRef, nil)
	return SentinelSendResult{OK: result.OK, MessageID: result.MessageID, Error: result.Error}, nil
}
