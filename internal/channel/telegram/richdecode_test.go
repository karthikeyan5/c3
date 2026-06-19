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
