package mappings

// LookupByCwd returns the mapping for an absolute, resolved cwd. The bool is
// false if no mapping exists.
func (mf *MappingsFile) LookupByCwd(cwd string) (Mapping, bool) {
	if mf == nil || mf.Mappings == nil {
		return Mapping{}, false
	}
	m, ok := mf.Mappings[cwd]
	return m, ok
}

// LookupTopicInDefaultGroup searches the channel's topic registry for a topic
// whose Name matches and whose Group equals the channel's DefaultGroup. The
// bool is false if the channel doesn't exist, has no default group, or the
// name isn't present in that group.
func (mf *MappingsFile) LookupTopicInDefaultGroup(channel, name string) (Topic, bool) {
	if mf == nil {
		return Topic{}, false
	}
	cc, ok := mf.Channels[channel]
	if !ok || cc.DefaultGroup == "" {
		return Topic{}, false
	}
	for _, tp := range cc.Topics {
		if tp.Name == name && tp.Group == cc.DefaultGroup {
			return tp, true
		}
	}
	return Topic{}, false
}

// LookupTopicAcrossGroups returns all topics in the channel whose Name
// matches, regardless of Group. Order is the order of the underlying slice.
// Returns an empty slice if the channel is unknown or no match is found.
func (mf *MappingsFile) LookupTopicAcrossGroups(channel, name string) []Topic {
	if mf == nil {
		return nil
	}
	cc, ok := mf.Channels[channel]
	if !ok {
		return nil
	}
	var hits []Topic
	for _, tp := range cc.Topics {
		if tp.Name == name {
			hits = append(hits, tp)
		}
	}
	return hits
}

// LookupTopicByID returns the topic registered for (chat_id, topic_id) in the
// given channel.
func (mf *MappingsFile) LookupTopicByID(channel string, chatID, topicID int64) (Topic, bool) {
	if mf == nil {
		return Topic{}, false
	}
	cc, ok := mf.Channels[channel]
	if !ok {
		return Topic{}, false
	}
	for _, tp := range cc.Topics {
		if tp.ChatID == chatID && tp.TopicID == topicID {
			return tp, true
		}
	}
	return Topic{}, false
}
