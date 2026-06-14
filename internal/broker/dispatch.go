package broker

import (
	"fmt"
	"log"
	"strings"
	"unicode/utf16"

	"github.com/karthikeyan5/c3/internal/c3types"
	"github.com/karthikeyan5/c3/internal/capability"
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
	markup, err := markupFromParseMode(argString(args, "parse_mode", ""))
	if err != nil {
		return nil, err
	}
	out := c3types.Outbound{
		Channel: key.Channel,
		ChatID:  argInt64(args, "chat_id", key.ChatID),
		TopicID: argTopicID(args, "topic_id", key),
		Text:    argString(args, "text", ""),
		Markup:  markup,
		Media:   mediaFromFilesArg(args),
	}
	if rt := argInt64Ptr(args, "reply_to"); rt != nil {
		out.ReplyTo = rt
	}

	// Route through the pure capability gate: it validates (hard-reject), down-
	// converts (e.g. markup→none), and splits the text into ordered parts that
	// each fit the channel's limits. A non-nil err is a HARD REJECTION — surface
	// it to the agent without sending anything (matching tool-error surfacing).
	parts, notes, alts, err := capability.Gate(ch.Capabilities(), out)
	if err != nil {
		return nil, err
	}

	// The gate is pure; dispatch (impure) writes the durable alteration log.
	for _, a := range alts {
		log.Printf("outbound-alteration chan=%s chat=%d topic=%s kind=%s detail=%q",
			key.Channel, out.ChatID, TopicKeyStr(key), a.Kind, a.Detail)
	}

	// Multi-part send contract: send parts sequentially, in order; on part-k
	// failure STOP (fail-fast) and report exactly how many landed. NEVER report
	// silent success. The agent-visible id is the FIRST part's id; reply-
	// threading/edits target the first part.
	var firstID int64
	for i, part := range parts {
		id, err := ch.SendReply(part)
		if i == 0 {
			firstID = id
		}
		if err != nil {
			if i == 0 {
				return nil, fmt.Errorf("send failed: %w", err)
			}
			// Some parts already landed: tell the agent precisely how far we got.
			return nil, fmt.Errorf("partial send: sent %d of %d; part %d failed: %w",
				i, len(parts), i+1, err)
		}
	}

	result := fmt.Sprintf("sent (id: %d)", firstID)
	if len(parts) > 1 {
		result = fmt.Sprintf("sent %d messages (first id: %d)", len(parts), firstID)
	}
	if len(notes) > 0 {
		result += "\n" + strings.Join(notes, "\n")
	}
	return mcpText(result), nil
}

// mediaFromFilesArg is a one-release back-compat shim translating the legacy
// `files` tool arg (a list of local paths) into channel-neutral Media items of
// Kind=file (byte-for-byte original delivery). No tool schema advertises or
// populates `files` today, so this returns nil in practice (behavior-preserving);
// it exists only to map a stray in-flight `files` arg until media sending lands
// in P3. Removed alongside the other shims in P7.
func mediaFromFilesArg(args map[string]any) []c3types.MediaItem {
	raw, ok := args["files"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil
	}
	var out []c3types.MediaItem
	for _, v := range list {
		p, ok := v.(string)
		if !ok || p == "" {
			continue
		}
		out = append(out, c3types.MediaItem{Kind: c3types.MediaFile, Path: p})
	}
	return out
}

// markupFromParseMode is a one-release back-compat shim translating the legacy
// `parse_mode` tool arg into the channel-neutral Markup intent, for in-flight
// sessions whose tool schemas still advertise `parse_mode`. The shim is removed
// in P7.
//
// Mapping:
//   - ""           → MarkupMarkdown (the converter renders agent markdown natively)
//   - "HTML"       → MarkupNative   (pre-formed channel markup; passthrough)
//   - "MarkdownV2" → reject with a clear note (the converter handles markdown
//     natively now; raw MarkdownV2 from the agent is rare and unsupported)
//
// Any other value is raw channel markup and is REJECTED unless it is the
// recognized native form ("HTML") — raw channel markup is only accepted via
// MarkupNative.
func markupFromParseMode(pm string) (c3types.Markup, error) {
	switch pm {
	case "":
		return c3types.MarkupMarkdown, nil
	case "HTML":
		return c3types.MarkupNative, nil
	case "MarkdownV2":
		return "", fmt.Errorf("parse_mode=MarkdownV2 is no longer supported: write standard markdown and omit parse_mode (it is converted for you), or pass parse_mode=HTML for pre-formed channel markup")
	default:
		return "", fmt.Errorf("parse_mode=%q is not a recognized channel markup: omit parse_mode for markdown, or pass parse_mode=HTML for pre-formed channel markup", pm)
	}
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
	markup, err := markupFromParseMode(argString(args, "parse_mode", ""))
	if err != nil {
		return nil, err
	}
	caps := ch.Capabilities()
	text := argString(args, "text", "")

	// Edits join the markup system but are SINGLE messages — they cannot be
	// split. Apply the same markup degradation the gate would (!RichText→none)
	// so an edit renders consistently with a reply, but reject (don't split) an
	// over-limit edit because there is no second message to overflow into.
	notes := []string{}
	if !caps.RichText && markup != c3types.MarkupNone {
		markup = c3types.MarkupNone
		notes = append(notes, "rich text is not supported on this channel — markdown was sent as plain text")
		log.Printf("outbound-alteration chan=%s chat=%d topic=%s kind=markup_degraded detail=%q",
			key.Channel, argInt64(args, "chat_id", key.ChatID), TopicKeyStr(key),
			"edit markup downgraded to none (channel RichText=false)")
	}
	if caps.MaxMessageRunes > 0 && utf16Len(text) > caps.MaxMessageRunes {
		return nil, fmt.Errorf("edit text is %d chars, over the channel's %d-char limit; an edit is a single message and cannot be split — shorten it or send a new reply instead",
			utf16Len(text), caps.MaxMessageRunes)
	}

	a := c3types.EditArgs{
		Channel:   key.Channel,
		ChatID:    argInt64(args, "chat_id", key.ChatID),
		MessageID: argInt64(args, "message_id", 0),
		Text:      text,
		Markup:    markup,
	}
	r, err := ch.EditMessage(a)
	if err != nil {
		return nil, err
	}
	result := fmt.Sprintf("edited (id: %d)", r.MessageID)
	if len(notes) > 0 {
		result += "\n" + strings.Join(notes, "\n")
	}
	return mcpText(result), nil
}

// utf16Len counts a string's length in UTF-16 code units — the unit channels
// like Telegram measure message length in. Used for the edit-over-limit check;
// the multi-part reply path measures inside the pure gate.
func utf16Len(s string) int {
	return len(utf16.Encode([]rune(s)))
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
