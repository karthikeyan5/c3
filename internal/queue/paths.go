// Package queue is C3's durable, per-route, append-only on-disk inbound queue.
// Every received Telegram inbound is persisted here (one JSONL line per message)
// before its update_id becomes eligible to advance the Telegram offset, so an
// accepted-but-undelivered message is never lost. The store is single-owner: all
// file operations for a route are funneled through that route's RouteWorker
// goroutine in the broker, so it holds no per-file locks.
package queue

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Caps — never silent (the broker logs + sends a Telegram notice on overflow).
const (
	// MaxMessages is the per-route line cap; EvictOverCap drops oldest beyond it.
	MaxMessages = 1000
	// MaxAge is the per-route age cap; EvictOverCap drops lines older than this.
	MaxAge = 14 * 24 * time.Hour
)

// RouteKey identifies one queued route. TopicID nil = DM / no topic.
type RouteKey struct {
	Channel string
	ChatID  int64
	TopicID *int64
}

// File returns the filesystem-safe basename (no extension) for this route:
// "<channel>__<chat_id>__<topic|none>". The store appends ".jsonl"/".cur".
func (rk RouteKey) File() string {
	topic := "none"
	if rk.TopicID != nil {
		topic = fmt.Sprintf("%d", *rk.TopicID)
	}
	return fmt.Sprintf("%s__%d__%s", rk.Channel, rk.ChatID, topic)
}

// QueueDir resolves the queue directory: $C3_QUEUE_DIR (override, tests), else
// $XDG_STATE_HOME/c3/queue, else ~/.local/state/c3/queue. Mirrors the offset
// store's XDG convention so queue files sit beside <channel>-offset.json.
func QueueDir() string {
	if env := os.Getenv("C3_QUEUE_DIR"); env != "" {
		return env
	}
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		return filepath.Join(x, "c3", "queue")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "c3", "queue")
}
