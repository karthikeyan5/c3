package broker

// RouteKey is the value-typed routing key. Spec §4.2 — replaces map[*int64]X
// (which would compare pointer identity, not value).
//
// nil topic-id distinguishes "no topic" (DM, non-forum group) from "General
// forum topic" (which is *int64(1) in user-visible types). At the map-key
// level this is encoded as HasTopic=false vs HasTopic=true with TopicID=1.
type RouteKey struct {
	Channel  string
	ChatID   int64
	HasTopic bool
	TopicID  int64
}

// MakeRouteKey converts a (channel, chat_id, *topic_id) triple into a
// value-typed, comparable, hashable RouteKey.
func MakeRouteKey(channel string, chatID int64, topicID *int64) RouteKey {
	if topicID == nil {
		return RouteKey{Channel: channel, ChatID: chatID, HasTopic: false}
	}
	return RouteKey{Channel: channel, ChatID: chatID, HasTopic: true, TopicID: *topicID}
}
