package mappings

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
