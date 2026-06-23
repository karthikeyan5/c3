package mappings

import "time"

// UpsertTopic inserts a new topic or updates an existing one (matched by
// channel + chat_id + topic_id). Creates the channel entry if missing.
func (mf *MappingsFile) UpsertTopic(channel string, t Topic) {
	if mf.Channels == nil {
		mf.Channels = map[string]ChannelConfig{}
	}
	cc := mf.Channels[channel]
	for i, existing := range cc.Topics {
		if existing.ChatID == t.ChatID && existing.TopicID == t.TopicID {
			cc.Topics[i] = t
			mf.Channels[channel] = cc
			return
		}
	}
	cc.Topics = append(cc.Topics, t)
	mf.Channels[channel] = cc
}

// UpsertMapping inserts a new cwd → mapping or updates an existing one. When
// updating, a zero CreatedAt on the new value is replaced with the existing
// entry's CreatedAt so update-flows can leave that field unset.
func (mf *MappingsFile) UpsertMapping(cwd string, m Mapping) {
	if mf.Mappings == nil {
		mf.Mappings = map[string]Mapping{}
	}
	if existing, ok := mf.Mappings[cwd]; ok {
		if m.CreatedAt.IsZero() {
			m.CreatedAt = existing.CreatedAt
		}
	}
	mf.Mappings[cwd] = m
}

// UpsertSessionAttachment records (or replaces) the last-attached route for a
// session id. The new value's Detached defaults false, so re-attaching after a
// detach clears the tombstone. No-op on an empty id.
func (mf *MappingsFile) UpsertSessionAttachment(id string, sa SessionAttachment) {
	if id == "" {
		return
	}
	if mf.SessionAttachments == nil {
		mf.SessionAttachments = map[string]SessionAttachment{}
	}
	mf.SessionAttachments[id] = sa
}

// LookupSessionAttachment returns the raw entry for a session id, if present.
// The caller applies the Recoverable policy (tombstone + TTL).
func (mf *MappingsFile) LookupSessionAttachment(id string) (SessionAttachment, bool) {
	if mf == nil || mf.SessionAttachments == nil || id == "" {
		return SessionAttachment{}, false
	}
	sa, ok := mf.SessionAttachments[id]
	return sa, ok
}

// TombstoneSessionAttachment marks a session's attachment as deliberately
// detached, so a later resume does NOT auto-recover it. No-op if absent.
func (mf *MappingsFile) TombstoneSessionAttachment(id string) {
	if mf == nil || mf.SessionAttachments == nil {
		return
	}
	if sa, ok := mf.SessionAttachments[id]; ok {
		sa.Detached = true
		mf.SessionAttachments[id] = sa
	}
}

// PruneSessionAttachments deletes entries older than ttl (since LastAttachedAt).
// Returns the count removed. Bounds growth of the store.
func (mf *MappingsFile) PruneSessionAttachments(now time.Time, ttl time.Duration) int {
	if mf == nil || mf.SessionAttachments == nil {
		return 0
	}
	n := 0
	for id, sa := range mf.SessionAttachments {
		if now.Sub(sa.LastAttachedAt) >= ttl {
			delete(mf.SessionAttachments, id)
			n++
		}
	}
	return n
}
