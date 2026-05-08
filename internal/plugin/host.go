// Package plugin defines the broker-side plugin extension API.
// Spec §4.5.1.
package plugin

import (
	"context"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// Host is what plugins receive from the broker.
type Host interface {
	OnInbound(fn func(ctx context.Context, msg *c3types.Inbound) (*c3types.Inbound, bool /*drop*/))
	OnVoiceReceived(fn func(ctx context.Context, payload c3types.VoicePayload) (string, error))
	OnOutbound(fn func(ctx context.Context, msg *c3types.Outbound) (*c3types.Outbound, bool /*drop*/))
	OnAttach(fn func(*Stub, *Mapping))

	RegisterTools(fn func(*ToolRegistry))

	Config(name string, target any) error
	State(name string) StateDir
	CacheDir(name string) string

	Channel(name string) (channel.Channel, error)

	Logf(format string, args ...any)
	Done() <-chan struct{}
}

type Stub struct {
	CLI    string
	PID    int
	CWD    string
	ConnID uint64
}

type Mapping struct {
	Channel string
	ChatID  int64
	TopicID *int64
	Name    string
	Group   string
}

type StateDir interface {
	Load(name string, target any) error
	Save(name string, target any) error
}

type ToolRegistry interface {
	Add(t Tool)
	Remove(name string)
	List() []Tool
}

type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     func(ctx context.Context, args map[string]any) (any, error)
}
