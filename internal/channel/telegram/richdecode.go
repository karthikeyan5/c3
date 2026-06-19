package telegram

// richdecode.go — inbound Bot API 10.1 rich-message decoder. ALL 10.1 inbound
// wire knowledge (RichMessage / RichBlock / RichText shapes) lives in this file
// (the R7 no-leak rule). The decoder turns the structured tree Telegram sends in
// Message.rich_message into GFM markdown the agent reads, plus any embedded media
// as downloadable attachments. gotgbot rc.34 has no typed RichMessage, so the
// caller hands us the raw json.RawMessage captured in poll.go.

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/karthikeyan5/c3/internal/c3types"
)

// richKind tags which of RichText's three JSON shapes a node holds.
type richKind int

const (
	richLeaf   richKind = iota // a bare JSON string
	richArray                  // an array of RichText
	richTagged                 // a {"type":..., "text":...} object
)

// richText is the parsed form of a RichText value. Exactly one shape per node.
type richText struct {
	kind  richKind
	text  string     // richLeaf
	items []richText // richArray
	typ   string     // richTagged: discriminator
	inner *richText  // richTagged: nested "text"
	url   string     // url
	name  string     // anchor/reference name
	alt   string     // custom_emoji alternative_text
	expr  string     // mathematical_expression
	user  int64      // text_mention user.id
}

// UnmarshalJSON handles RichText's three shapes: string | array | tagged object.
// Unknown/garbage shapes decode to an empty leaf (never an error) so the whole
// decode degrades gracefully rather than failing.
func (rt *richText) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		rt.kind = richLeaf
		return nil
	}
	switch data[0] {
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		rt.kind = richLeaf
		rt.text = s
	case '[':
		var arr []richText
		if err := json.Unmarshal(data, &arr); err != nil {
			return err
		}
		rt.kind = richArray
		rt.items = arr
	case '{':
		var obj struct {
			Type            string    `json:"type"`
			Text            *richText `json:"text"`
			URL             string    `json:"url"`
			Name            string    `json:"name"`
			AnchorName      string    `json:"anchor_name"`
			ReferenceName   string    `json:"reference_name"`
			AlternativeText string    `json:"alternative_text"`
			Expression      string    `json:"expression"`
			User            *struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		if err := json.Unmarshal(data, &obj); err != nil {
			return err
		}
		rt.kind = richTagged
		rt.typ = obj.Type
		rt.inner = obj.Text
		rt.url = obj.URL
		rt.name = firstNonEmpty(obj.Name, obj.AnchorName, obj.ReferenceName)
		rt.alt = obj.AlternativeText
		rt.expr = obj.Expression
		if obj.User != nil {
			rt.user = obj.User.ID
		}
	default:
		rt.kind = richLeaf
	}
	return nil
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// inlineEscaper escapes the markdown-structural characters that would otherwise
// make literal text misrender (a deliberate LIGHT set — readability over perfect
// round-trip; see spec §5.4).
var inlineEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"*", `\*`,
	"_", `\_`,
	"[", `\[`,
	"]", `\]`,
	"|", `\|`,
	"~", `\~`,
)

func escapeInline(s string) string { return inlineEscaper.Replace(s) }

// renderRichText renders a RichText node to GFM markdown, escaping plain text.
func renderRichText(rt *richText) string {
	if rt == nil {
		return ""
	}
	switch rt.kind {
	case richLeaf:
		return escapeInline(rt.text)
	case richArray:
		var b strings.Builder
		for i := range rt.items {
			b.WriteString(renderRichText(&rt.items[i]))
		}
		return b.String()
	case richTagged:
		inner := renderRichText(rt.inner)
		switch rt.typ {
		case "bold":
			return "**" + inner + "**"
		case "italic":
			return "*" + inner + "*"
		case "underline":
			return "__" + inner + "__"
		case "strikethrough":
			return "~~" + inner + "~~"
		case "spoiler":
			return "||" + inner + "||"
		case "marked":
			return "==" + inner + "=="
		case "code":
			return "`" + plainText(rt.inner) + "`"
		case "url":
			return "[" + inner + "](" + rt.url + ")"
		case "text_mention":
			return "[" + inner + "](tg://user?id=" + strconv.FormatInt(rt.user, 10) + ")"
		case "custom_emoji":
			return escapeInline(rt.alt)
		case "mathematical_expression":
			return "$" + rt.expr + "$"
		case "anchor", "anchor_link", "reference", "reference_link":
			return "[" + inner + "](#" + rt.name + ")"
		default:
			// subscript/superscript/date_time and all auto-detected entities
			// (mention/hashtag/cashtag/bot_command/email/phone/bank_card) plus
			// any future inline type → render their text literally.
			return inner
		}
	}
	return ""
}

// plainText returns a node's text WITHOUT markdown escaping — used for inline
// code spans where the content is literal monospace.
func plainText(rt *richText) string {
	if rt == nil {
		return ""
	}
	switch rt.kind {
	case richLeaf:
		return rt.text
	case richArray:
		var b strings.Builder
		for i := range rt.items {
			b.WriteString(plainText(&rt.items[i]))
		}
		return b.String()
	case richTagged:
		switch rt.typ {
		case "custom_emoji":
			return rt.alt
		case "mathematical_expression":
			return rt.expr
		default:
			return plainText(rt.inner)
		}
	}
	return ""
}

// _ ensures c3types is imported for future tasks that extend this file with
// block/table/media handling (those use c3types.Attachment etc.).
var _ = (*c3types.Capabilities)(nil)
