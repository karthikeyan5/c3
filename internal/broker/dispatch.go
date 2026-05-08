package broker

import (
	"fmt"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/channel"
)

// dispatchTool translates a tool-call (name, args) into a channel method call.
// Returns the MCP-shape result map (with content[].text or attachment_path
// fields) and any error. The route key is used to fill in chat_id/topic_id
// when the args don't provide them.
func dispatchTool(ch channel.Channel, key RouteKey, tool string, args map[string]any) (map[string]any, error) {
	switch tool {
	case "reply":
		return dispatchReply(ch, key, args)
	case "react":
		return dispatchReact(ch, key, args)
	case "edit_message":
		return dispatchEditMessage(ch, key, args)
	case "send_typing":
		return dispatchSendTyping(ch, key, args)
	case "download_attachment":
		return dispatchDownloadAttachment(ch, args)
	default:
		return nil, fmt.Errorf("unknown tool %q", tool)
	}
}

func dispatchReply(ch channel.Channel, key RouteKey, args map[string]any) (map[string]any, error) {
	r := c3types.ReplyArgs{
		Channel: key.Channel,
		ChatID:  argInt64(args, "chat_id", key.ChatID),
		TopicID: argTopicID(args, "topic_id", key),
		Text:    argString(args, "text", ""),
	}
	if pm := argString(args, "parse_mode", ""); pm != "" {
		r.ParseMode = pm
	}
	if rt := argInt64Ptr(args, "reply_to"); rt != nil {
		r.ReplyTo = rt
	}
	id, err := ch.SendReply(r)
	if err != nil {
		return nil, err
	}
	return mcpText(fmt.Sprintf("sent (id: %d)", id)), nil
}

func dispatchReact(ch channel.Channel, key RouteKey, args map[string]any) (map[string]any, error) {
	a := c3types.ReactArgs{
		Channel:   key.Channel,
		ChatID:    argInt64(args, "chat_id", key.ChatID),
		MessageID: argInt64(args, "message_id", 0),
		Emoji:     argString(args, "emoji", ""),
	}
	if err := ch.React(a); err != nil {
		return nil, err
	}
	return mcpText("reacted"), nil
}

func dispatchEditMessage(ch channel.Channel, key RouteKey, args map[string]any) (map[string]any, error) {
	a := c3types.EditArgs{
		Channel:   key.Channel,
		ChatID:    argInt64(args, "chat_id", key.ChatID),
		MessageID: argInt64(args, "message_id", 0),
		Text:      argString(args, "text", ""),
		ParseMode: argString(args, "parse_mode", ""),
	}
	r, err := ch.EditMessage(a)
	if err != nil {
		return nil, err
	}
	return mcpText(fmt.Sprintf("edited (id: %d)", r.MessageID)), nil
}

func dispatchSendTyping(ch channel.Channel, key RouteKey, args map[string]any) (map[string]any, error) {
	chatID := argInt64(args, "chat_id", key.ChatID)
	tid := argTopicID(args, "topic_id", key)
	if err := ch.SendTyping(chatID, tid); err != nil {
		return nil, err
	}
	return mcpText("typing"), nil
}

func dispatchDownloadAttachment(ch channel.Channel, args map[string]any) (map[string]any, error) {
	fid := argString(args, "file_id", "")
	if fid == "" {
		return nil, fmt.Errorf("download_attachment: file_id required")
	}
	path, err := ch.DownloadAttachment(fid)
	if err != nil {
		return nil, err
	}
	return mcpText(path), nil
}

// mcpText returns the standard MCP-shape result with one text entry.
func mcpText(s string) map[string]any {
	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": s},
		},
	}
}

// argString returns the string value at key, or fallback. Tolerates non-string
// values (e.g. int) by stringifying.
func argString(args map[string]any, key, fallback string) string {
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case string:
			return x
		case fmt.Stringer:
			return x.String()
		default:
			return fmt.Sprintf("%v", x)
		}
	}
	return fallback
}

// argInt64 returns the int64 value at key, or fallback. Accepts float64
// (json default for numbers) and string forms.
func argInt64(args map[string]any, key string, fallback int64) int64 {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		var n int64
		_, err := fmt.Sscanf(x, "%d", &n)
		if err == nil {
			return n
		}
	}
	return fallback
}

// argInt64Ptr returns &v if args[key] is set and parseable; nil otherwise.
func argInt64Ptr(args map[string]any, key string) *int64 {
	if _, ok := args[key]; !ok {
		return nil
	}
	v := argInt64(args, key, 0)
	return &v
}

// argTopicID returns *int64 for topic_id arg, falling back to the route key's
// HasTopic+TopicID if the arg is absent. nil means no topic (DM).
func argTopicID(args map[string]any, key string, route RouteKey) *int64 {
	if _, ok := args[key]; ok {
		v := argInt64(args, key, 0)
		if v == 0 {
			return nil
		}
		return &v
	}
	if !route.HasTopic {
		return nil
	}
	v := route.TopicID
	return &v
}
