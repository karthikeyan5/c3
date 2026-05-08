// Package channel defines the contract every transport implements.
// Spec §4.1.
package channel

import (
	"context"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// Channel is the contract every transport implements. Methods are called by
// the broker on its own goroutine — implementations must be safe for
// concurrent use, except Start/Stop which are sequenced.
type Channel interface {
	Name() string
	Start(ctx context.Context, host Host) error
	Stop() error

	SendReply(args c3types.ReplyArgs) (sentMessageID int64, err error)
	SendTyping(chatID int64, threadID *int64) error
	EditMessage(args c3types.EditArgs) (*c3types.EditResult, error)
	React(args c3types.ReactArgs) error
	DownloadAttachment(fileID string) (path string, err error)

	// Topic management (Telegram-specific in v1; future channels may stub).
	CreateTopic(chatID int64, name string) (topicID int64, err error)
	ValidateTopic(chatID int64, threadID int64) error
}

// Host is what the broker passes to a Channel. Subset of plugin.Host scoped
// to channel concerns (config + emit + log + done).
type Host interface {
	Config(name string, target any) error
	Emit(in *c3types.Inbound)
	Logf(format string, args ...any)
	Done() <-chan struct{}
}
