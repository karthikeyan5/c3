# Rich Message Inbound Decode — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Decode Bot API 10.1 inbound rich messages (`Message.rich_message`, a `RichBlock`/`RichText` tree) into GFM markdown plus downloadable attachments, so rich messages never reach the agent empty.

**Architecture:** A new self-contained decoder (`richdecode.go`) turns the rich tree into markdown + `[]c3types.Attachment`. The poll loop captures the `rich_message` raw JSON that gotgbot rc.34 silently drops (raw `getUpdates` + a second probe unmarshal), threads it to `convertInbound`, which decodes rich-first. A per-channel `rich_inbound` config flag (default on) gates decoding; a `DeliversRichMessages` capability flag advertises it.

**Tech Stack:** Go; gotgbot v2 rc.34 (via its raw `RequestWithContext` bridge); standard `encoding/json`.

## Global Constraints

- **R7 no-leak:** ALL Bot API 10.1 wire knowledge (type names, field shapes) stays inside `internal/channel/telegram/`. No rich type/constant leaks to `core`/`broker`.
- **Hard invariants:** a present `rich_message` NEVER yields empty `Text`; the decoder NEVER panics the poll loop (top-level `recover()`); malformed input degrades to a marker, never a crash.
- **gotgbot pin:** `github.com/PaulSonOfLars/gotgbot/v2 v2.0.0-rc.34` — do NOT bump. Use the raw `c.bot.RequestWithContext(ctx, method, params, opts)` bridge (same as `sendrich.go`).
- **Config default-true trap:** the toggle is `*bool` (nil ⇒ true). NEVER a bare `bool` (zero-values to false, silently disabling).
- **Commit trailer (every commit):** `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- **Branch:** `feat/rich-message-inbound`.
- **Run all tests** with `go test ./...` from `~/arogara/c3`; package tests with `go test ./internal/channel/telegram/ -run <Name> -v`.

---

### Task 1: Config toggle (`rich_inbound`, default on)

**Files:**
- Modify: `internal/mappings/types.go` (add field to `ChannelConfig`)
- Modify: `internal/mappings/clone.go` (`cloneChannelConfig` deep-copy)
- Modify: `internal/channel/telegram/telegram.go` (add field to `Config` + accessor)
- Test: `internal/mappings/clone_test.go` (new test)
- Test: `internal/channel/telegram/richconfig_test.go` (new file)

**Interfaces:**
- Produces: `mappings.ChannelConfig.RichInbound *bool` and `telegram.Config.RichInbound *bool` (json tag `rich_inbound`); `(telegram.Config).RichInboundEnabled() bool` (nil ⇒ true).

- [ ] **Step 1: Write the failing config-accessor + clone tests**

Create `internal/channel/telegram/richconfig_test.go`:

```go
package telegram

import "testing"

func TestRichInboundEnabled(t *testing.T) {
	if !(Config{}).RichInboundEnabled() {
		t.Error("nil RichInbound should default to true (enabled)")
	}
	yes := true
	if !(Config{RichInbound: &yes}).RichInboundEnabled() {
		t.Error("RichInbound=true should be enabled")
	}
	no := false
	if (Config{RichInbound: &no}).RichInboundEnabled() {
		t.Error("RichInbound=false should be disabled")
	}
}
```

Append to `internal/mappings/clone_test.go`:

```go
func TestClone_PreservesRichInbound(t *testing.T) {
	no := false
	original := &MappingsFile{
		SchemaVersion: 1,
		Channels:      map[string]ChannelConfig{"telegram": {RichInbound: &no}},
	}
	clone := original.Clone()
	cc := clone.Channels["telegram"]
	if cc.RichInbound == nil {
		t.Fatal("clone dropped ChannelConfig.RichInbound")
	}
	if *cc.RichInbound != false {
		t.Errorf("clone RichInbound = %v, want false", *cc.RichInbound)
	}
	// Deep copy: mutating the clone must not touch the original.
	*cc.RichInbound = true
	if *original.Channels["telegram"].RichInbound != false {
		t.Error("clone leak: mutating clone changed original RichInbound")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run TestRichInboundEnabled -v && go test ./internal/mappings/ -run TestClone_PreservesRichInbound -v`
Expected: FAIL (compile error — `RichInbound` / `RichInboundEnabled` undefined).

- [ ] **Step 3: Add the field to `mappings.ChannelConfig`**

In `internal/mappings/types.go`, inside `ChannelConfig`, after the `APIBaseURLs` field:

```go
	// RichInbound gates decoding of inbound Bot API 10.1 rich messages into
	// markdown (telegram channel). nil/absent ⇒ true (enabled). A bare bool
	// would zero-value to false and silently disable decoding for everyone who
	// never set it — the trap documented for notifications.invasive.
	RichInbound *bool `json:"rich_inbound,omitempty"`
```

- [ ] **Step 4: Deep-copy it in `cloneChannelConfig`**

In `internal/mappings/clone.go`, inside `cloneChannelConfig`, after the `Topics` block and before `return out`:

```go
	if cc.RichInbound != nil {
		v := *cc.RichInbound
		out.RichInbound = &v
	}
```

- [ ] **Step 5: Add the field + accessor to `telegram.Config`**

In `internal/channel/telegram/telegram.go`, inside `Config`, after the `APIBaseURLs` field:

```go
	// RichInbound gates decoding of inbound rich messages. nil/absent ⇒ true.
	// Bridged from mappings.ChannelConfig via host.Config (json.Marshal →
	// json.Unmarshal); the json tag MUST match the mappings side.
	RichInbound *bool `json:"rich_inbound,omitempty"`
```

Add the accessor (place it near the `Config` type):

```go
// RichInboundEnabled reports whether inbound rich-message decoding is on.
// Absent config (nil) ⇒ true (decode by default).
func (c Config) RichInboundEnabled() bool {
	return c.RichInbound == nil || *c.RichInbound
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run TestRichInboundEnabled -v && go test ./internal/mappings/ -run TestClone_PreservesRichInbound -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/mappings/types.go internal/mappings/clone.go internal/channel/telegram/telegram.go internal/channel/telegram/richconfig_test.go internal/mappings/clone_test.go
git commit -m "feat(rich-inbound): add rich_inbound config toggle (default on)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Inline `RichText` decoder

**Files:**
- Create: `internal/channel/telegram/richdecode.go`
- Test: `internal/channel/telegram/richdecode_test.go`

**Interfaces:**
- Produces: type `richText` with `UnmarshalJSON`; `renderRichText(rt *richText) string`; `plainText(rt *richText) string`; `escapeInline(s string) string`. Used by Tasks 3–5.

- [ ] **Step 1: Write the failing inline tests**

Create `internal/channel/telegram/richdecode_test.go`:

```go
package telegram

import (
	"encoding/json"
	"testing"
)

func renderRT(t *testing.T, jsonStr string) string {
	t.Helper()
	var rt richText
	if err := json.Unmarshal([]byte(jsonStr), &rt); err != nil {
		t.Fatalf("unmarshal %s: %v", jsonStr, err)
	}
	return renderRichText(&rt)
}

func TestRenderRichText_Inline(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", `"hello"`, "hello"},
		{"array", `["a","b"]`, "ab"},
		{"bold", `{"type":"bold","text":"x"}`, "**x**"},
		{"italic", `{"type":"italic","text":"x"}`, "*x*"},
		{"underline", `{"type":"underline","text":"x"}`, "__x__"},
		{"strike", `{"type":"strikethrough","text":"x"}`, "~~x~~"},
		{"spoiler", `{"type":"spoiler","text":"x"}`, "||x||"},
		{"code", `{"type":"code","text":"a*b"}`, "`a*b`"},
		{"marked", `{"type":"marked","text":"x"}`, "==x=="},
		{"url", `{"type":"url","text":"site","url":"https://e.com"}`, "[site](https://e.com)"},
		{"mention_user", `{"type":"text_mention","text":"Al","user":{"id":7}}`, "[Al](tg://user?id=7)"},
		{"custom_emoji", `{"type":"custom_emoji","alternative_text":"🔥","custom_emoji_id":"1"}`, "🔥"},
		{"math", `{"type":"mathematical_expression","expression":"x^2"}`, "$x^2$"},
		{"reference", `{"type":"reference","text":"see","name":"fn1"}`, "[see](#fn1)"},
		{"hashtag_literal", `{"type":"hashtag","text":"#go","hashtag":"go"}`, "#go"},
		{"nested_bold_italic", `{"type":"bold","text":{"type":"italic","text":"x"}}`, "***x***"},
		{"unknown_inline_passthrough", `{"type":"newfangled","text":"keep"}`, "keep"},
		{"null_empty", `null`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderRT(t, c.in); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestEscapeInline(t *testing.T) {
	// Structural chars in plain text get escaped so they are not misread.
	if got := renderRT(t, `"a*b_c|d"`); got != `a\*b\_c\|d` {
		t.Errorf("escape: got %q", got)
	}
	// Code content is NOT escaped (raw monospace).
	if got := renderRT(t, `{"type":"code","text":"a*b|c"}`); got != "`a*b|c`" {
		t.Errorf("code raw: got %q", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run 'TestRenderRichText_Inline|TestEscapeInline' -v`
Expected: FAIL (compile error — `richText` undefined).

- [ ] **Step 3: Write `richdecode.go` inline core**

Create `internal/channel/telegram/richdecode.go`:

```go
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
	items []richText  // richArray
	typ   string     // richTagged: discriminator
	inner *richText   // richTagged: nested "text"
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
```

> Note: `nested_bold_italic` expects `***x***` — bold wrapping italic yields `**` + `*x*` + `**` = `***x***`. ✓

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run 'TestRenderRichText_Inline|TestEscapeInline' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/telegram/richdecode.go internal/channel/telegram/richdecode_test.go
git commit -m "feat(rich-inbound): inline RichText decoder (tree -> GFM)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Structural `RichBlock` decoder (non-table, non-media)

**Files:**
- Modify: `internal/channel/telegram/richdecode.go`
- Test: `internal/channel/telegram/richdecode_test.go`

**Interfaces:**
- Consumes: `renderRichText`, `plainText` (Task 2).
- Produces: types `richBlock`, `richListItem`, `richCell`, `photoSize`, `fileObj`, `richBlockCaption`; `renderBlock(b *richBlock) (string, []c3types.Attachment)`; `renderBlocks(blocks []richBlock) (string, []c3types.Attachment)`; `prefixLines(s, prefix string) string`. Tables (Task 4) and media (Task 5) fill in their `renderBlock` cases.

- [ ] **Step 1: Write the failing structural-block tests**

Append to `internal/channel/telegram/richdecode_test.go`:

```go
func renderBlk(t *testing.T, jsonStr string) string {
	t.Helper()
	var b richBlock
	if err := json.Unmarshal([]byte(jsonStr), &b); err != nil {
		t.Fatalf("unmarshal %s: %v", jsonStr, err)
	}
	md, _ := renderBlock(&b)
	return md
}

func TestRenderBlock_Structural(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"paragraph", `{"type":"paragraph","text":"hi there"}`, "hi there"},
		{"heading1", `{"type":"heading","size":1,"text":"Title"}`, "# Title"},
		{"heading3", `{"type":"heading","size":3,"text":"Sub"}`, "### Sub"},
		{"heading_clamp_low", `{"type":"heading","size":0,"text":"X"}`, "# X"},
		{"heading_clamp_high", `{"type":"heading","size":9,"text":"X"}`, "###### X"},
		{"pre", `{"type":"pre","language":"go","text":"a := 1"}`, "```go\na := 1\n```"},
		{"divider", `{"type":"divider"}`, "---"},
		{"math_block", `{"type":"mathematical_expression","expression":"e=mc^2"}`, "$$e=mc^2$$"},
		{"footer", `{"type":"footer","text":"end"}`, "---\n*end*"},
		{"blockquote", `{"type":"blockquote","blocks":[{"type":"paragraph","text":"q"}],"credit":"me"}`, "> q\n> — me"},
		{"pullquote", `{"type":"pullquote","text":"big","credit":"src"}`, "> big\n> — src"},
		{"details", `{"type":"details","summary":"More","blocks":[{"type":"paragraph","text":"body"}]}`, "**More**\n\nbody"},
		{"anchor_empty", `{"type":"anchor","name":"x"}`, ""},
		{"thinking_empty", `{"type":"thinking","text":"hidden"}`, ""},
		{"unknown_with_text", `{"type":"futureblock","text":"salvage"}`, "salvage"},
		{"unknown_bare", `{"type":"weird"}`, "[unsupported block: weird]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := renderBlk(t, c.in); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestRenderList(t *testing.T) {
	unordered := `{"type":"list","items":[
		{"blocks":[{"type":"paragraph","text":"one"}]},
		{"blocks":[{"type":"paragraph","text":"two"}]}]}`
	if got := renderBlk(t, unordered); got != "- one\n- two" {
		t.Errorf("unordered: %q", got)
	}
	checkbox := `{"type":"list","items":[
		{"has_checkbox":true,"is_checked":true,"blocks":[{"type":"paragraph","text":"done"}]},
		{"has_checkbox":true,"is_checked":false,"blocks":[{"type":"paragraph","text":"todo"}]}]}`
	if got := renderBlk(t, checkbox); got != "- [x] done\n- [ ] todo" {
		t.Errorf("checkbox: %q", got)
	}
	ordered := `{"type":"list","items":[
		{"type":"1","value":1,"blocks":[{"type":"paragraph","text":"a"}]},
		{"type":"1","value":2,"blocks":[{"type":"paragraph","text":"b"}]}]}`
	if got := renderBlk(t, ordered); got != "1. a\n2. b" {
		t.Errorf("ordered: %q", got)
	}
}

func TestRenderBlocks_JoinsWithBlankLine(t *testing.T) {
	var blocks []richBlock
	if err := json.Unmarshal([]byte(`[
		{"type":"heading","size":1,"text":"T"},
		{"type":"paragraph","text":"p"}]`), &blocks); err != nil {
		t.Fatal(err)
	}
	md, _ := renderBlocks(blocks)
	if md != "# T\n\np" {
		t.Errorf("got %q", md)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run 'TestRenderBlock_Structural|TestRenderList|TestRenderBlocks' -v`
Expected: FAIL (compile error — `richBlock` undefined).

- [ ] **Step 3: Add block types + structural rendering to `richdecode.go`**

Append to `internal/channel/telegram/richdecode.go`:

```go
// richBlock is a single RichBlock. A union over all block types; only the fields
// for its Type are populated. Caption is raw because it has two shapes: RichText
// (table) vs RichBlockCaption (media) — decoded per-type by the handler.
type richBlock struct {
	Type       string          `json:"type"`
	Text       *richText       `json:"text"`
	Size       int             `json:"size"`        // heading: 1=largest..6
	Language   string          `json:"language"`    // pre
	Blocks     []richBlock     `json:"blocks"`      // blockquote/details/collage/slideshow
	Credit     *richText       `json:"credit"`      // blockquote/pullquote
	Summary    *richText       `json:"summary"`     // details
	Items      []richListItem  `json:"items"`       // list
	Cells      [][]richCell    `json:"cells"`       // table
	Expression string          `json:"expression"`  // math
	Name       string          `json:"name"`        // anchor
	Caption    json.RawMessage `json:"caption"`     // table=RichText, media=RichBlockCaption
	Photo      []photoSize     `json:"photo"`       // media
	Video      *fileObj        `json:"video"`
	Animation  *fileObj        `json:"animation"`
	Audio      *fileObj        `json:"audio"`
	VoiceNote  *fileObj        `json:"voice_note"`
}

type richListItem struct {
	Label       string      `json:"label"`
	Blocks      []richBlock `json:"blocks"`
	HasCheckbox bool        `json:"has_checkbox"`
	IsChecked   bool        `json:"is_checked"`
	Value       int         `json:"value"`
	Type        string      `json:"type"` // "a"/"A"/"i"/"I"/"1"
}

type richCell struct {
	Text     *richText `json:"text"`
	IsHeader bool      `json:"is_header"`
	Colspan  int       `json:"colspan"`
	Rowspan  int       `json:"rowspan"`
	Align    string    `json:"align"`
	Valign   string    `json:"valign"`
}

type photoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int64  `json:"width"`
	Height   int64  `json:"height"`
}

type fileObj struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	MimeType string `json:"mime_type"`
	FileName string `json:"file_name"`
}

type richBlockCaption struct {
	Text   *richText `json:"text"`
	Credit *richText `json:"credit"`
}

// renderBlock renders one block to markdown and any embedded media attachments.
func renderBlock(b *richBlock) (string, []c3types.Attachment) {
	switch b.Type {
	case "paragraph":
		return renderRichText(b.Text), nil
	case "heading":
		level := b.Size
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		return strings.Repeat("#", level) + " " + renderRichText(b.Text), nil
	case "pre":
		return "```" + b.Language + "\n" + plainText(b.Text) + "\n```", nil
	case "divider":
		return "---", nil
	case "mathematical_expression":
		return "$$" + b.Expression + "$$", nil
	case "footer":
		return "---\n*" + renderRichText(b.Text) + "*", nil
	case "blockquote":
		body, atts := renderBlocks(b.Blocks)
		out := prefixLines(body, "> ")
		if b.Credit != nil {
			out += "\n> — " + renderRichText(b.Credit)
		}
		return out, atts
	case "pullquote":
		out := prefixLines(renderRichText(b.Text), "> ")
		if b.Credit != nil {
			out += "\n> — " + renderRichText(b.Credit)
		}
		return out, nil
	case "details":
		body, atts := renderBlocks(b.Blocks)
		return "**" + renderRichText(b.Summary) + "**\n\n" + body, atts
	case "list":
		return renderList(b.Items)
	case "collage", "slideshow":
		return renderBlocks(b.Blocks)
	case "anchor", "thinking":
		// anchor has no readable text; thinking is outbound-only (never inbound).
		return "", nil
	case "table":
		return renderTable(b), nil // Task 4
	case "map":
		return "[map]", nil
	case "photo", "video", "animation", "audio", "voice_note":
		return renderMedia(b) // Task 5
	default:
		// Graceful degradation for unknown/future block types: salvage any text
		// or child blocks; never silently empty.
		if b.Text != nil {
			return renderRichText(b.Text), nil
		}
		if len(b.Blocks) > 0 {
			return renderBlocks(b.Blocks)
		}
		return "[unsupported block: " + b.Type + "]", nil
	}
}

// renderBlocks renders a slice of blocks, joining non-empty results with a blank
// line and aggregating their attachments in order.
func renderBlocks(blocks []richBlock) (string, []c3types.Attachment) {
	var parts []string
	var atts []c3types.Attachment
	for i := range blocks {
		md, a := renderBlock(&blocks[i])
		if md != "" {
			parts = append(parts, md)
		}
		atts = append(atts, a...)
	}
	return strings.Join(parts, "\n\n"), atts
}

// renderList renders a RichBlockList's items as GFM list lines.
func renderList(items []richListItem) (string, []c3types.Attachment) {
	var lines []string
	var atts []c3types.Attachment
	for i := range items {
		it := &items[i]
		marker := "-"
		switch {
		case it.HasCheckbox && it.IsChecked:
			marker = "- [x]"
		case it.HasCheckbox:
			marker = "- [ ]"
		case it.Type != "" || it.Value != 0:
			n := it.Value
			if n == 0 {
				n = i + 1
			}
			marker = strconv.Itoa(n) + "."
		}
		body, a := renderBlocks(it.Blocks)
		atts = append(atts, a...)
		if body == "" {
			body = escapeInline(it.Label)
		}
		lines = append(lines, marker+" "+body)
	}
	return strings.Join(lines, "\n"), atts
}

// prefixLines prefixes every line of s with prefix (used for blockquotes).
func prefixLines(s, prefix string) string {
	if s == "" {
		return prefix
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}
```

> This references `renderTable` (Task 4) and `renderMedia` (Task 5), which do not exist yet — the package will NOT compile until Task 4 and Task 5 add them. To keep this task independently green, add temporary stubs now and DELETE them in their owning tasks:

```go
// TEMP STUB — removed in Task 4.
func renderTable(b *richBlock) string { return "[table]" }

// TEMP STUB — removed in Task 5.
func renderMedia(b *richBlock) (string, []c3types.Attachment) { return "[media]", nil }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run 'TestRenderBlock_Structural|TestRenderList|TestRenderBlocks' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/telegram/richdecode.go internal/channel/telegram/richdecode_test.go
git commit -m "feat(rich-inbound): structural RichBlock decoder + temp table/media stubs

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Table decoder (`RichBlockTable` → GFM pipe table)

**Files:**
- Modify: `internal/channel/telegram/richdecode.go` (replace the `renderTable` stub)
- Test: `internal/channel/telegram/richdecode_test.go`

**Interfaces:**
- Consumes: `richBlock`, `richCell`, `renderRichText` (Tasks 2–3).
- Produces: real `renderTable(b *richBlock) string` (replaces the stub).

- [ ] **Step 1: Write the failing table tests**

Append to `internal/channel/telegram/richdecode_test.go`:

```go
func TestRenderTable(t *testing.T) {
	// Header row (is_header), one body row, per-column alignment.
	in := `{"type":"table","cells":[
		[{"text":"Name","is_header":true,"align":"left"},{"text":"Age","is_header":true,"align":"right"}],
		[{"text":"Al","align":"left"},{"text":"30","align":"right"}]]}`
	want := "| Name | Age |\n| :-- | --: |\n| Al | 30 |"
	if got := renderBlk(t, in); got != want {
		t.Errorf("table:\n got %q\nwant %q", got, want)
	}
}

func TestRenderTable_NoHeaderSynthesizesOne(t *testing.T) {
	in := `{"type":"table","cells":[
		[{"text":"a"},{"text":"b"}],
		[{"text":"c"},{"text":"d"}]]}`
	// No is_header cell: synthesize an empty header so GFM still renders.
	want := "|  |  |\n| --- | --- |\n| a | b |\n| c | d |"
	if got := renderBlk(t, in); got != want {
		t.Errorf("noheader:\n got %q\nwant %q", got, want)
	}
}

func TestRenderTable_OmittedCellAndCenterAlign(t *testing.T) {
	in := `{"type":"table","cells":[
		[{"text":"H","is_header":true,"align":"center"},{"is_header":true,"align":"center"}],
		[{"text":"x","align":"center"},{"text":"y","align":"center"}]]}`
	want := "| H |  |\n| :-: | :-: |\n| x | y |"
	if got := renderBlk(t, in); got != want {
		t.Errorf("omitted:\n got %q\nwant %q", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run TestRenderTable -v`
Expected: FAIL (stub returns `[table]`).

- [ ] **Step 3: Replace the `renderTable` stub with the real implementation**

In `internal/channel/telegram/richdecode.go`, DELETE the temp `renderTable` stub and add:

```go
// renderTable renders a RichBlockTable as a GFM pipe table. The header is the
// first row whose cells are is_header; if no row is a header, an empty header is
// synthesized (GFM requires one). colspan/rowspan cannot be expressed in GFM —
// each cell renders in its primary position and spans are left blank (documented
// lossy degradation, spec §5.3). align maps left→:--, center→:-:, right→--:.
func renderTable(b *richBlock) string {
	rows := b.Cells
	if len(rows) == 0 {
		return ""
	}
	// Column count = widest row.
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return ""
	}
	// Find the header row (first all/any is_header row); default to none.
	headerIdx := -1
	for i, r := range rows {
		for _, c := range r {
			if c.IsHeader {
				headerIdx = i
				break
			}
		}
		if headerIdx >= 0 {
			break
		}
	}

	cell := func(r []richCell, ci int) string {
		if ci < len(r) {
			return renderRichText(r[ci].Text)
		}
		return ""
	}
	align := func(r []richCell, ci int) string {
		a := ""
		if ci < len(r) {
			a = r[ci].Align
		}
		switch a {
		case "center":
			return ":-:"
		case "right":
			return "--:"
		case "left":
			return ":--"
		default:
			return "---"
		}
	}
	rowLine := func(r []richCell) string {
		fields := make([]string, cols)
		for ci := 0; ci < cols; ci++ {
			fields[ci] = cell(r, ci)
		}
		return "| " + strings.Join(fields, " | ") + " |"
	}

	var out []string
	var headerRow []richCell
	var bodyRows [][]richCell
	if headerIdx >= 0 {
		headerRow = rows[headerIdx]
		for i, r := range rows {
			if i != headerIdx {
				bodyRows = append(bodyRows, r)
			}
		}
	} else {
		headerRow = nil // synthesize empty header
		bodyRows = rows
	}

	out = append(out, rowLine(headerRow))
	// Delimiter row: use header's per-column align when present, else "---".
	delims := make([]string, cols)
	for ci := 0; ci < cols; ci++ {
		delims[ci] = align(headerRow, ci)
	}
	out = append(out, "| "+strings.Join(delims, " | ")+" |")
	for _, r := range bodyRows {
		out = append(out, rowLine(r))
	}
	return strings.Join(out, "\n")
}
```

> Verify against the test expectations: an empty header row with `cols=2` renders `|  |  |` (each empty field surrounded by spaces) ✓; default delimiter `---` ✓.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run TestRenderTable -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/telegram/richdecode.go internal/channel/telegram/richdecode_test.go
git commit -m "feat(rich-inbound): RichBlockTable -> GFM pipe table

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Media blocks + top-level `decodeRichMessage` (invariants + recover)

**Files:**
- Modify: `internal/channel/telegram/richdecode.go` (replace `renderMedia` stub; add `decodeRichMessage`)
- Test: `internal/channel/telegram/richdecode_test.go`

**Interfaces:**
- Consumes: all of Tasks 2–4.
- Produces: real `renderMedia(b *richBlock) (string, []c3types.Attachment)`; `decodeRichMessage(raw json.RawMessage) (markdown string, atts []c3types.Attachment, ok bool)`.

- [ ] **Step 1: Write the failing media + top-level tests**

Append to `internal/channel/telegram/richdecode_test.go`:

```go
func TestRenderMedia_Photo(t *testing.T) {
	in := `{"type":"photo","photo":[
		{"file_id":"small","file_size":10,"width":100,"height":100},
		{"file_id":"big","file_size":99,"width":1000,"height":1000}],
		"caption":{"text":"a cat"}}`
	var b richBlock
	if err := json.Unmarshal([]byte(in), &b); err != nil {
		t.Fatal(err)
	}
	md, atts := renderMedia(&b)
	if md != "[photo: a cat]" {
		t.Errorf("marker: %q", md)
	}
	if len(atts) != 1 || atts[0].Kind != "photo" || atts[0].FileID != "big" {
		t.Fatalf("atts: %+v", atts)
	}
	if atts[0].Size != 99 {
		t.Errorf("size: %d", atts[0].Size)
	}
}

func TestRenderMedia_VideoNoCaption(t *testing.T) {
	in := `{"type":"video","video":{"file_id":"v1","file_size":5,"mime_type":"video/mp4"}}`
	var b richBlock
	if err := json.Unmarshal([]byte(in), &b); err != nil {
		t.Fatal(err)
	}
	md, atts := renderMedia(&b)
	if md != "[video]" {
		t.Errorf("marker: %q", md)
	}
	if len(atts) != 1 || atts[0].Kind != "video" || atts[0].FileID != "v1" || atts[0].MIME != "video/mp4" {
		t.Fatalf("atts: %+v", atts)
	}
}

func TestDecodeRichMessage_FullDocument(t *testing.T) {
	raw := json.RawMessage(`{"blocks":[
		{"type":"heading","size":1,"text":"Report"},
		{"type":"paragraph","text":"See table:"},
		{"type":"table","cells":[
			[{"text":"K","is_header":true,"align":"left"},{"text":"V","is_header":true,"align":"left"}],
			[{"text":"a","align":"left"},{"text":"1","align":"left"}]]}]}`)
	md, atts, ok := decodeRichMessage(raw)
	if !ok {
		t.Fatal("ok=false for valid doc")
	}
	want := "# Report\n\nSee table:\n\n| K | V |\n| :-- | :-- |\n| a | 1 |"
	if md != want {
		t.Errorf("md:\n got %q\nwant %q", md, want)
	}
	if len(atts) != 0 {
		t.Errorf("unexpected atts: %+v", atts)
	}
}

func TestDecodeRichMessage_Invariants(t *testing.T) {
	// Malformed JSON → ok=false.
	if _, _, ok := decodeRichMessage(json.RawMessage(`{not json`)); ok {
		t.Error("malformed JSON should give ok=false")
	}
	// Empty/no-content tree → marker, ok=true (never empty when rich present).
	md, _, ok := decodeRichMessage(json.RawMessage(`{"blocks":[]}`))
	if !ok || md != "[rich message]" {
		t.Errorf("empty tree: md=%q ok=%v", md, ok)
	}
	// Unknown block alone → its marker, never empty.
	md2, _, ok2 := decodeRichMessage(json.RawMessage(`{"blocks":[{"type":"mystery"}]}`))
	if !ok2 || md2 != "[unsupported block: mystery]" {
		t.Errorf("unknown: md=%q ok=%v", md2, ok2)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run 'TestRenderMedia|TestDecodeRichMessage' -v`
Expected: FAIL (stub `renderMedia` returns `[media]`; `decodeRichMessage` undefined).

- [ ] **Step 3: Replace `renderMedia` stub + add `decodeRichMessage`**

In `internal/channel/telegram/richdecode.go`, DELETE the temp `renderMedia` stub and add:

```go
// renderMedia maps a media block to a downloadable Attachment plus an inline
// text marker at its position. Mirrors convertInbound's media mapping so the
// existing download path + size caps apply unchanged.
func renderMedia(b *richBlock) (string, []c3types.Attachment) {
	var att c3types.Attachment
	switch b.Type {
	case "photo":
		if len(b.Photo) == 0 {
			return "[photo]", nil
		}
		best := b.Photo[0]
		bestArea := best.Width * best.Height
		for _, p := range b.Photo[1:] {
			if a := p.Width * p.Height; a > bestArea {
				best, bestArea = p, a
			}
		}
		att = c3types.Attachment{Kind: "photo", FileID: best.FileID, Size: best.FileSize}
	case "video":
		att = fileAttachment("video", b.Video)
	case "animation":
		att = fileAttachment("animation", b.Animation)
	case "audio":
		att = fileAttachment("audio", b.Audio)
	case "voice_note":
		att = fileAttachment("voice", b.VoiceNote)
	}
	marker := "[" + b.Type + "]"
	if cap := mediaCaption(b.Caption); cap != "" {
		marker = "[" + b.Type + ": " + cap + "]"
	}
	if att.FileID == "" {
		return marker, nil
	}
	return marker, []c3types.Attachment{att}
}

func fileAttachment(kind string, f *fileObj) c3types.Attachment {
	if f == nil {
		return c3types.Attachment{}
	}
	return c3types.Attachment{
		Kind:   kind,
		FileID: f.FileID,
		Size:   f.FileSize,
		MIME:   f.MimeType,
		Name:   f.FileName,
	}
}

// mediaCaption decodes a media block's caption (a RichBlockCaption object) into
// plain marker text. Returns "" if absent or undecodable.
func mediaCaption(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var c richBlockCaption
	if err := json.Unmarshal(raw, &c); err != nil {
		return ""
	}
	return strings.TrimSpace(plainText(c.Text))
}

// richMessage is the inbound RichMessage envelope: blocks + is_rtl.
type richMessage struct {
	Blocks []richBlock `json:"blocks"`
	IsRTL  bool        `json:"is_rtl"`
}

// decodeRichMessage parses a Bot API 10.1 rich_message payload into GFM markdown
// plus embedded media attachments. ok=false on malformed JSON (caller falls back
// to a marker). When the tree is present but yields no content, returns the
// "[rich message]" marker with ok=true — a present rich_message NEVER yields
// empty text. NEVER panics: a top-level recover turns any panic into ok=false.
func decodeRichMessage(raw json.RawMessage) (markdown string, atts []c3types.Attachment, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			markdown, atts, ok = "", nil, false
		}
	}()
	if len(raw) == 0 {
		return "", nil, false
	}
	var rm richMessage
	if err := json.Unmarshal(raw, &rm); err != nil {
		return "", nil, false
	}
	md, a := renderBlocks(rm.Blocks)
	md = strings.TrimSpace(md)
	if md == "" && len(a) == 0 {
		return "[rich message]", nil, true
	}
	return md, a, true
}
```

> The `TestDecodeRichMessage_FullDocument` expectation: heading `# Report`, paragraph `See table:`, then the table — joined by `renderBlocks` with `\n\n`. The table's header cells are `is_header` with `align:"left"` → delimiter `:--`. ✓

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run 'TestRenderMedia|TestDecodeRichMessage' -v`
Expected: PASS.

- [ ] **Step 5: Run the full telegram package to confirm no stub remnants**

Run: `go test ./internal/channel/telegram/ -v 2>&1 | tail -20`
Expected: PASS (no leftover `[table]`/`[media]` stub behavior).

- [ ] **Step 6: Commit**

```bash
git add internal/channel/telegram/richdecode.go internal/channel/telegram/richdecode_test.go
git commit -m "feat(rich-inbound): media blocks -> attachments + decodeRichMessage entry

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Integrate the decoder into `convertInbound`

**Files:**
- Modify: `internal/channel/telegram/inbound.go` (signature + rich-first branch)
- Test: `internal/channel/telegram/inbound_test.go`

**Interfaces:**
- Consumes: `decodeRichMessage` (Task 5).
- Produces: new signature `convertInbound(channel string, msg *gotgbot.Message, sttPrefix string, richRaw json.RawMessage) *c3types.Inbound`. (Task 7 supplies `richRaw`.)

- [ ] **Step 1: Update existing tests to the new signature + add rich tests**

In `internal/channel/telegram/inbound_test.go`, the existing tests call `convertInbound("telegram", msg, "")` and `convertInbound("telegram", msg, "[Transcribed voice]: ")`. Add a trailing `nil` argument to EVERY existing `convertInbound(...)` call in this file (the new `richRaw` param). Then append:

```go
func TestConvertInbound_RichMessage(t *testing.T) {
	msg := &gotgbot.Message{
		MessageId: 5,
		From:      &gotgbot.User{Id: 42, Username: "alice"},
		Chat:      gotgbot.Chat{Id: -100},
		Date:      1715151931,
		// No Text — the body is in rich_message (captured separately by poll.go).
	}
	rich := json.RawMessage(`{"blocks":[{"type":"heading","size":1,"text":"Hi"},{"type":"paragraph","text":"there"}]}`)
	in := convertInbound("telegram", msg, "", rich)
	if in == nil {
		t.Fatal("nil")
	}
	if in.Text != "# Hi\n\nthere" {
		t.Errorf("Text=%q", in.Text)
	}
}

func TestConvertInbound_RichMessageWithMedia(t *testing.T) {
	msg := &gotgbot.Message{MessageId: 6, From: &gotgbot.User{Id: 1}, Chat: gotgbot.Chat{Id: -100}}
	rich := json.RawMessage(`{"blocks":[{"type":"photo","photo":[{"file_id":"pid","file_size":3,"width":9,"height":9}],"caption":{"text":"pic"}}]}`)
	in := convertInbound("telegram", msg, "", rich)
	if in.Text != "[photo: pic]" {
		t.Errorf("Text=%q", in.Text)
	}
	if len(in.Attachments) != 1 || in.Attachments[0].FileID != "pid" {
		t.Fatalf("Attachments=%+v", in.Attachments)
	}
}

func TestConvertInbound_NoRichRawUsesPlainText(t *testing.T) {
	msg := &gotgbot.Message{MessageId: 7, From: &gotgbot.User{Id: 1}, Chat: gotgbot.Chat{Id: -100}, Text: "plain"}
	in := convertInbound("telegram", msg, "", nil) // toggle-off / non-rich path
	if in.Text != "plain" {
		t.Errorf("Text=%q", in.Text)
	}
}

func TestConvertInbound_RichDecodeFailFallsBackToMarker(t *testing.T) {
	msg := &gotgbot.Message{MessageId: 8, From: &gotgbot.User{Id: 1}, Chat: gotgbot.Chat{Id: -100}}
	in := convertInbound("telegram", msg, "", json.RawMessage(`{bad`))
	if in.Text != "[rich message]" {
		t.Errorf("Text=%q", in.Text)
	}
}
```

Add `"encoding/json"` to the imports of `inbound_test.go` if not present.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run TestConvertInbound -v`
Expected: FAIL (compile error — too few args / new tests undefined behavior).

- [ ] **Step 3: Update `convertInbound`**

In `internal/channel/telegram/inbound.go`:

1. Add `"encoding/json"` to the imports.
2. Change the signature to:

```go
func convertInbound(channel string, msg *gotgbot.Message, sttPrefix string, richRaw json.RawMessage) *c3types.Inbound {
```

3. Immediately AFTER the `in := &c3types.Inbound{...}` literal and the `TopicID`/`ReplyTo` population, but BEFORE the `// Body + attachments` switch, insert the rich-first branch:

```go
	// Rich message (Bot API 10.1) takes precedence: a rich message IS the
	// message. richRaw is non-nil only when the channel captured a rich_message
	// AND the rich_inbound toggle is on (see poll.go). Decode to markdown +
	// attachments; on decode failure fall back to a non-empty marker so a rich
	// message is never surfaced empty.
	if len(richRaw) > 0 {
		md, atts, ok := decodeRichMessage(richRaw)
		if !ok {
			md = "[rich message]"
		}
		in.Text = md
		in.Attachments = append(in.Attachments, atts...)
		return in
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run TestConvertInbound -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/channel/telegram/inbound.go internal/channel/telegram/inbound_test.go
git commit -m "feat(rich-inbound): decode rich messages in convertInbound (rich-first)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Capture `rich_message` raw JSON in the poll loop + wire the toggle

**Files:**
- Modify: `internal/channel/telegram/poll.go` (raw getUpdates + probe; thread richRaw through `dispatchUpdate`/`dispatchMessage`)
- Test: `internal/channel/telegram/poll_richprobe_test.go` (new file — pure helper tests, no network)

**Interfaces:**
- Consumes: `convertInbound(..., richRaw)` (Task 6); `(Config).RichInboundEnabled()` (Task 1).
- Produces: `parseUpdates(raw []byte) ([]gotgbot.Update, []updateProbe, error)`; `richRawFor(p updateProbe) json.RawMessage`; updated `dispatchUpdate(u *gotgbot.Update, richRaw json.RawMessage)` and `dispatchMessage(updateID int64, msg *gotgbot.Message, edited bool, richRaw json.RawMessage)`.

- [ ] **Step 1: Write the failing pure-helper tests**

Create `internal/channel/telegram/poll_richprobe_test.go`:

```go
package telegram

import (
	"encoding/json"
	"testing"
)

func TestParseUpdates_PairsRichByIndex(t *testing.T) {
	// One plain text update, one with a rich_message — same array, same order.
	raw := []byte(`[
		{"update_id":1,"message":{"message_id":10,"chat":{"id":-100},"text":"plain"}},
		{"update_id":2,"message":{"message_id":11,"chat":{"id":-100},"rich_message":{"blocks":[{"type":"paragraph","text":"hi"}]}}}]`)
	ups, probes, err := parseUpdates(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ups) != 2 || len(probes) != 2 {
		t.Fatalf("len ups=%d probes=%d", len(ups), len(probes))
	}
	if ups[0].UpdateId != 1 || ups[1].UpdateId != 2 {
		t.Errorf("update ids: %d %d", ups[0].UpdateId, ups[1].UpdateId)
	}
	if rr := richRawFor(probes[0]); len(rr) != 0 {
		t.Errorf("update 0 should have no rich: %s", rr)
	}
	rr := richRawFor(probes[1])
	if len(rr) == 0 {
		t.Fatal("update 1 should carry rich_message raw")
	}
	md, _, ok := decodeRichMessage(rr)
	if !ok || md != "hi" {
		t.Errorf("decoded rich: md=%q ok=%v", md, ok)
	}
}

func TestRichRawFor_EditedMessage(t *testing.T) {
	var p updateProbe
	if err := json.Unmarshal([]byte(`{"edited_message":{"rich_message":{"blocks":[{"type":"paragraph","text":"e"}]}}}`), &p); err != nil {
		t.Fatal(err)
	}
	if len(richRawFor(p)) == 0 {
		t.Error("edited_message rich_message not captured")
	}
}

func TestParseUpdates_Malformed(t *testing.T) {
	if _, _, err := parseUpdates([]byte(`{not an array`)); err == nil {
		t.Error("expected error on malformed array")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/channel/telegram/ -run 'TestParseUpdates|TestRichRawFor' -v`
Expected: FAIL (compile error — `parseUpdates`/`updateProbe`/`richRawFor` undefined).

- [ ] **Step 3: Add the probe types + pure parse helpers to `poll.go`**

In `internal/channel/telegram/poll.go`, ensure `"encoding/json"` is imported, then add (near the top, after imports):

```go
// updateProbe captures ONLY the rich_message raw JSON that gotgbot rc.34 drops
// during typed unmarshal (it has no RichMessage field). We unmarshal the raw
// getUpdates response a second time into this minimal shape and pair it with the
// typed updates by array index.
type updateProbe struct {
	Message       *messageProbe `json:"message"`
	EditedMessage *messageProbe `json:"edited_message"`
}

type messageProbe struct {
	RichMessage json.RawMessage `json:"rich_message"`
}

// parseUpdates unmarshals a raw getUpdates result array into BOTH the typed
// gotgbot updates (the existing downstream path, byte-identical to what
// Bot.GetUpdates produced) and the rich-message probes (same array, same order).
// Pure — unit-tested without network.
func parseUpdates(raw []byte) ([]gotgbot.Update, []updateProbe, error) {
	var updates []gotgbot.Update
	if err := json.Unmarshal(raw, &updates); err != nil {
		return nil, nil, err
	}
	var probes []updateProbe
	// Best-effort: if the probe unmarshal somehow fails, proceed with no rich
	// data rather than dropping the whole batch.
	_ = json.Unmarshal(raw, &probes)
	return updates, probes, nil
}

// richRawFor returns the rich_message raw JSON for an update's message (or edited
// message), or nil if absent.
func richRawFor(p updateProbe) json.RawMessage {
	if p.Message != nil && len(p.Message.RichMessage) > 0 {
		return p.Message.RichMessage
	}
	if p.EditedMessage != nil && len(p.EditedMessage.RichMessage) > 0 {
		return p.EditedMessage.RichMessage
	}
	return nil
}
```

- [ ] **Step 4: Run helper tests to verify they pass**

Run: `go test ./internal/channel/telegram/ -run 'TestParseUpdates|TestRichRawFor' -v`
Expected: PASS.

- [ ] **Step 5: Switch the poll loop to the raw getUpdates call**

In `internal/channel/telegram/poll.go`, replace the GetUpdates call site:

```go
		opts := &gotgbot.GetUpdatesOpts{
			Offset:         offset,
			Timeout:        longPoll,
			AllowedUpdates: allowedUpdates,
			RequestOpts:    c.requestOptsFor("getUpdates"),
		}
		updates, err := c.bot.GetUpdates(opts)
```

with:

```go
		params := map[string]any{
			"offset":          offset,
			"timeout":         longPoll,
			"allowed_updates": allowedUpdates,
		}
		raw, err := c.bot.RequestWithContext(c.ctx, "getUpdates", params, c.requestOptsFor("getUpdates"))
		var updates []gotgbot.Update
		var probes []updateProbe
		if err == nil {
			updates, probes, err = parseUpdates(raw)
		}
```

> Rationale (spec §7, R-1): `requestOptsFor("getUpdates")` already sets the long-poll-safe timeout (`longPoll + 30s`) and the active endpoint — identical to before. `RequestWithContext` is the same lower layer `Bot.GetUpdates` uses, so `classifyError` sees the same error types. `allowed_updates` is JSON-marshaled as an array exactly as `GetUpdatesOpts` does. Offset handling below is unchanged.

- [ ] **Step 6: Thread richRaw through the success loop + dispatch**

In `pollLoop`, the success loop currently does `c.dispatchUpdate(&u)` inside `for _, u := range updates`. Change the loop to index-pair with probes:

```go
		var advanced bool
		for i := range updates {
			u := updates[i]
			if c.dedup != nil && c.dedup.SeenOrAdd(&u) {
				c.host.Logf("telegram: dedup skip update=%d (recent duplicate)", u.UpdateId)
				if u.UpdateId >= offset {
					offset = u.UpdateId + 1
					advanced = true
				}
				continue
			}
			var richRaw json.RawMessage
			if i < len(probes) {
				richRaw = richRawFor(probes[i])
			}
			c.dispatchUpdate(&u, richRaw)
			if u.UpdateId >= offset {
				offset = u.UpdateId + 1
				advanced = true
			}
		}
```

Update `dispatchUpdate` (only message/edited carry rich content):

```go
func (c *Channel) dispatchUpdate(u *gotgbot.Update, richRaw json.RawMessage) {
	switch {
	case u.Message != nil:
		c.dispatchMessage(u.UpdateId, u.Message, false, richRaw)
	case u.EditedMessage != nil:
		c.dispatchMessage(u.UpdateId, u.EditedMessage, true, richRaw)
	case u.Poll != nil:
		c.dispatchPollUpdate(u.UpdateId, u.Poll)
	case u.CallbackQuery != nil:
		c.dispatchCallback(u.UpdateId, u.CallbackQuery)
	case u.MessageReaction != nil:
		c.dispatchReaction(u.UpdateId, u.MessageReaction)
	default:
		c.host.Logf("telegram: drop update=%d (subscribed type with no dispatch handler)", u.UpdateId)
	}
}
```

Update `dispatchMessage` signature + the `convertInbound` call (gate by the toggle):

```go
func (c *Channel) dispatchMessage(updateID int64, msg *gotgbot.Message, edited bool, richRaw json.RawMessage) {
	if !c.cfg.RichInboundEnabled() {
		richRaw = nil // toggle off ⇒ rich messages surface as today (empty)
	}
	in := convertInbound(c.Name(), msg, c.cfg.STTPrefix, richRaw)
```

(Leave the rest of `dispatchMessage` unchanged.)

- [ ] **Step 7: Check any other callers of `dispatchUpdate`/`dispatchMessage`/`convertInbound`**

Run: `grep -rn "dispatchUpdate\|dispatchMessage\|convertInbound" internal/channel/telegram/ | grep -v "_test.go"`
Update any non-test caller to the new signatures. (Tests for `dispatchUpdate`/`dispatchMessage`, if any, are updated in the next step.)

- [ ] **Step 8: Update any affected existing tests, then build + full package test**

Run: `go test ./internal/channel/telegram/ 2>&1 | tail -30`
If existing tests call `dispatchUpdate`/`dispatchMessage` with the old arity, add the trailing `nil` argument. Expected after fixes: PASS.

- [ ] **Step 9: Full build + vet**

Run: `go build ./... && go vet ./internal/channel/telegram/ && go test ./... 2>&1 | tail -30`
Expected: build clean; all tests PASS.

- [ ] **Step 10: Commit**

```bash
git add internal/channel/telegram/poll.go internal/channel/telegram/poll_richprobe_test.go
git commit -m "feat(rich-inbound): capture rich_message raw in poll loop + wire toggle

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Advertise inbound rich support in the capability manifest

**Files:**
- Modify: `internal/c3types/caps.go` (add `DeliversRichMessages` to `InboundCaps`)
- Modify: `internal/channel/telegram/capabilities.go` (set it)
- Test: `internal/channel/telegram/capabilities_test.go`

**Interfaces:**
- Produces: `c3types.InboundCaps.DeliversRichMessages bool`, set `true` in the telegram manifest.

- [ ] **Step 1: Write the failing capability test**

Append to `internal/channel/telegram/capabilities_test.go`:

```go
func TestCapabilities_DeliversRichMessages(t *testing.T) {
	caps := (&Channel{}).Capabilities()
	if !caps.Inbound.DeliversRichMessages {
		t.Error("telegram should advertise DeliversRichMessages=true")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/channel/telegram/ -run TestCapabilities_DeliversRichMessages -v`
Expected: FAIL (compile error — field undefined).

- [ ] **Step 3: Add the field to `InboundCaps`**

In `internal/c3types/caps.go`, inside `InboundCaps`, after `DeliversCallbacks bool`:

```go
	// DeliversRichMessages reports that the channel decodes inbound Bot API 10.1
	// rich messages (Message.rich_message) into the agent-facing Text + media
	// attachments, rather than surfacing them empty.
	DeliversRichMessages bool
```

- [ ] **Step 4: Set it in the telegram manifest**

In `internal/channel/telegram/capabilities.go`, inside the `Inbound: c3types.InboundCaps{...}` literal, after `DeliversCallbacks: true,`:

```go
				DeliversRichMessages: true,
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/channel/telegram/ -run TestCapabilities_DeliversRichMessages -v`
Expected: PASS.

- [ ] **Step 6: Full suite + commit**

```bash
go test ./...
git add internal/c3types/caps.go internal/channel/telegram/capabilities.go internal/channel/telegram/capabilities_test.go
git commit -m "feat(rich-inbound): advertise DeliversRichMessages inbound capability

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Post-implementation verification (after all tasks)

- [ ] `go build ./... && go test ./...` — all green.
- [ ] `go vet ./...` — clean.
- [ ] **R-1 live-poll check (critical):** rebuild (`go install ./cmd/...`), restart the broker, confirm the log shows `telegram: connected` and that a normal text message still round-trips end-to-end (the raw `getUpdates` swap did not regress polling/offset/timeout). A quiet long-poll that returns zero updates must NOT be logged as a failure.
- [ ] Send (or simulate) a rich message and confirm it surfaces as markdown, not empty.
- [ ] PII audit before any push: `~/arogara/pii-audit/scan.sh ~/arogara/c3`.

## Self-Review (completed by plan author)

**Spec coverage:** §3 wire facts → Tasks 2–5 types/handlers. §4 architecture → all tasks. §5.1 blocks → Tasks 3–5. §5.2 inline → Task 2. §5.3 tables → Task 4. §5.4 escaping → Task 2 (`escapeInline`). §6 invariants → Task 5 (`decodeRichMessage` recover/markers) + Task 3 (unknown block) + Task 6 (decode-fail fallback). §7 raw capture → Task 7. §8 config + caps → Task 1 + Task 8. §9 integration → Task 6. §10 security/R7 → all decode code in `richdecode.go`; media via existing download path. §11 testing → tests in every task. ✓ no gaps.

**Placeholder scan:** the only non-final code is the TWO clearly-labeled TEMP STUBs in Task 3, each explicitly deleted in its owning task (4 and 5). No TBD/TODO/"handle errors" placeholders.

**Type consistency:** `richText`, `richBlock`, `richListItem`, `richCell`, `photoSize` (`FileID`), `fileObj` (`FileID`), `richBlockCaption`, `richMessage`, `updateProbe`/`messageProbe` consistent across tasks. `decodeRichMessage(json.RawMessage) (string, []c3types.Attachment, bool)` used identically in Tasks 5/6/7. `convertInbound(..., richRaw json.RawMessage)` consistent Tasks 6/7. `RichInboundEnabled()` consistent Tasks 1/7. ✓
