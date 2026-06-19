package telegram

import (
	"encoding/json"
	"strings"
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

// TestEscapeInline_FullCharSet pins EVERY structural character escapeInline
// handles, so a future edit to inlineEscaper can't silently drop one. The set is
// the eight chars: \ ` * _ [ ] | ~ — each gets a single leading backslash.
func TestEscapeInline_FullCharSet(t *testing.T) {
	in := "\\`*_[]|~" // the 8 structural chars, in order
	want := "\\\\" + "\\`" + "\\*" + "\\_" + "\\[" + "\\]" + "\\|" + "\\~"
	if got := escapeInline(in); got != want {
		t.Errorf("escapeInline(%q)\n got %q\nwant %q", in, got, want)
	}
}

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

// TestRenderTable_RaggedRow covers a body row NARROWER than the header (fewer
// cells than the column count): the missing trailing cells render as empty,
// padded to the table width so the GFM grid stays rectangular.
func TestRenderTable_RaggedRow(t *testing.T) {
	in := `{"type":"table","cells":[
		[{"text":"A","is_header":true},{"text":"B","is_header":true},{"text":"C","is_header":true}],
		[{"text":"x"},{"text":"y"}]]}`
	want := "| A | B | C |\n| --- | --- | --- |\n| x | y |  |"
	if got := renderBlk(t, in); got != want {
		t.Errorf("ragged:\n got %q\nwant %q", got, want)
	}
}

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

// TestDecodeRichMessage_DeepBlocksNoPanicMarker drives a block tree nested far
// past maxDecodeDepth (Telegram never sends this; it is the untrusted-input
// contract). The decoder must (a) NOT panic — the depth guard + the top-level
// recover() are the backstops — and (b) cut the recursion off with depthMarker
// rather than walking the whole tree. A valid-but-deep tree still decodes
// (ok=true).
func TestDecodeRichMessage_DeepBlocksNoPanicMarker(t *testing.T) {
	n := maxDecodeDepth + 50
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(`{"type":"blockquote","blocks":[`)
	}
	sb.WriteString(`{"type":"paragraph","text":"deep"}`)
	for i := 0; i < n; i++ {
		sb.WriteString(`]}`)
	}
	raw := json.RawMessage(`{"blocks":[` + sb.String() + `]}`)
	md, _, ok := decodeRichMessage(raw)
	if !ok {
		t.Fatal("deep-but-valid tree should decode (ok=true), got ok=false")
	}
	if !strings.Contains(md, depthMarker) {
		t.Errorf("expected depth marker %q in output; got %q", depthMarker, md)
	}
}

// TestRenderRichText_DeepInlineMarker is the inline (RichText) analogue: deeply
// nested inline tags also cut off with depthMarker instead of recursing without
// bound.
func TestRenderRichText_DeepInlineMarker(t *testing.T) {
	n := maxDecodeDepth + 10
	in := strings.Repeat(`{"type":"bold","text":`, n) + `"x"` + strings.Repeat(`}`, n)
	if got := renderRT(t, in); !strings.Contains(got, depthMarker) {
		t.Errorf("expected depth marker in deeply-nested inline text; got %q", got)
	}
}
