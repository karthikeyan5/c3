package broker

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// HandleCommand handles broker-owned Telegram bot commands. Currently only
// "/status" (and "/status@<bot>") is wired — a tiny dispatcher that is trivially
// extensible later (/drain, /clear) but YAGNI for now. A "/status" sent in a
// topic returns that topic's status; in DM/General it returns the global
// summary. Anything else returns ("", false) so the channel routes normally.
func (h *BrokerHost) HandleCommand(in *c3types.Inbound) (string, bool) {
	if in == nil {
		return "", false
	}
	cmd := strings.TrimSpace(in.Text)
	if i := strings.IndexByte(cmd, '@'); i >= 0 { // strip /status@botname
		cmd = cmd[:i]
	}
	if !strings.EqualFold(cmd, "/status") {
		return "", false
	}
	if in.TopicID != nil {
		return h.broker.statusForTopic(in.Channel, in.ChatID, in.TopicID), true
	}
	return h.broker.statusGlobal(), true
}

// statusForTopic renders the per-topic status line.
//
// I7: reads the mutex-guarded in-memory status index (StatusFor) — NOT the queue
// files via Pending — so a /status answered on a poll goroutine never races the
// route worker's concurrent Append/Consume/rewrite (the global StatusAll path was
// already index-based; this brings the per-topic path to the same single-owner
// discipline).
func (b *Broker) statusForTopic(channelName string, chatID int64, topicID *int64) string {
	key := MakeRouteKey(channelName, chatID, topicID)
	name := b.topicDisplayName(channelName, chatID, topicID)
	pending, oldest := 0, time.Time{}
	if b.Queue != nil {
		st := b.Queue.StatusFor(queueRouteKey(key))
		pending = st.Pending
		if st.OldestUnix > 0 {
			oldest = time.Unix(st.OldestUnix, 0)
		}
	}
	attached := "no CLI attached"
	if h, held := b.Routes.Holder(key); held {
		if h.IsAlive() {
			attached = "CLI attached"
		} else {
			// Dead reference: the holder's adapter is gone (disconnected AND its
			// PID is no longer in the OS process table). Verify liveness at READ
			// time and reap the stale claim on the spot so the status we print is
			// honest instead of trusting a cached map entry that only gets swept
			// lazily on the next inbound. Release is connID-guarded, so a live
			// re-claim landing between Holder and here is never clobbered.
			b.Routes.Release(key, h.ConnID)
		}
	}
	return fmt.Sprintf("📊 %s · %d queued%s · %s · broker up", name, pending, oldestSuffix(oldest), attached)
}

// statusGlobal renders the broker-wide summary (empty queues omitted).
func (b *Broker) statusGlobal() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "📊 Broker up (pid %d).", os.Getpid())
	if b.Queue == nil {
		return sb.String()
	}
	all := b.Queue.StatusAll()
	if len(all) > 0 {
		sb.WriteString(" Active queues:")
		type row struct {
			name    string
			pending int
			oldest  int64
		}
		rows := make([]row, 0, len(all))
		for k, st := range all {
			rows = append(rows, row{b.topicDisplayName(k.Channel, k.ChatID, k.TopicID), st.Pending, st.OldestUnix})
		}
		sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
		for _, r := range rows {
			fmt.Fprintf(&sb, "\n• %s — %d%s", r.name, r.pending, oldestSuffix(time.Unix(r.oldest, 0)))
		}
	}
	attached, idle := b.sessionCounts()
	fmt.Fprintf(&sb, "\n%d attached · %d idle", attached, idle)
	return sb.String()
}

// topicDisplayName looks up the topic's friendly name; falls back to "dm" or
// "topic-<id>".
func (b *Broker) topicDisplayName(channelName string, chatID int64, topicID *int64) string {
	if topicID == nil {
		return "dm"
	}
	if tp, ok := b.Mappings().LookupTopicByID(channelName, chatID, *topicID); ok && tp.Name != "" {
		return tp.Name
	}
	return fmt.Sprintf("topic-%d", *topicID)
}

// sessionCounts returns (attached, idle) live agent-session counts.
func (b *Broker) sessionCounts() (attached, idle int) {
	for _, s := range b.Stubs.Snapshot() {
		if s.CLI == "c3-broker-cli" {
			continue
		}
		if !s.IsAlive() {
			continue // dead session (disconnected + PID gone): count it as neither
		}
		if s.CurrentRoute() != nil {
			attached++
		} else {
			idle++
		}
	}
	return attached, idle
}

// oldestSuffix renders " (oldest 2h)" or "" when there is nothing queued.
func oldestSuffix(oldest time.Time) string {
	if oldest.IsZero() || oldest.Unix() <= 0 {
		return ""
	}
	d := time.Since(oldest)
	switch {
	case d < time.Minute:
		return " (oldest <1m)"
	case d < time.Hour:
		return fmt.Sprintf(" (oldest %dm)", int(d.Minutes()))
	default:
		return fmt.Sprintf(" (oldest %dh)", int(d.Hours()))
	}
}
