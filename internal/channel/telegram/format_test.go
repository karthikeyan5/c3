package telegram

import "testing"

func TestMdToTelegramHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		// --- Plain text + escaping ---
		{"plain", "hello world", "hello world"},
		{"angle-escape", "a < b > c & d", "a &lt; b &gt; c &amp; d"},
		{"literal-tag-escaped", "text with <tag> & amp", "text with &lt;tag&gt; &amp; amp"},

		// --- Bold ---
		{"bold-stars", "say **hi** there", "say <b>hi</b> there"},
		{"bold-unders", "say __hi__ there", "say <b>hi</b> there"},
		{"two-bolds", "**a** then **b**", "<b>a</b> then <b>b</b>"},
		{"bold-with-html-chars", "**a < b**", "<b>a &lt; b</b>"},

		// --- Italic ---
		{"italic-star", "an *emphatic* word", "an <i>emphatic</i> word"},
		{"italic-under", "an _emphatic_ word", "an <i>emphatic</i> word"},
		{"bold-and-italic", "**b** and *i*", "<b>b</b> and <i>i</i>"},

		// --- Strikethrough / spoiler ---
		{"strike", "this is ~~gone~~ now", "this is <s>gone</s> now"},
		{"spoiler", "the answer is ||42||", `the answer is <span class="tg-spoiler">42</span>`},

		// --- Inline code (literal content, no markdown inside) ---
		{"inline-code", "use `c3-broker status`", "use <code>c3-broker status</code>"},
		{"code-with-html", "see `<b>` tag", "see <code>&lt;b&gt;</code> tag"},
		{"bold-inside-code-not-bolded", "`**not bold**`", "<code>**not bold**</code>"},
		{"under-inside-code-literal", "`a_b_c`", "<code>a_b_c</code>"},

		// --- Fenced code blocks ---
		{"fenced-bare", "```\ncode here\n```", "<pre>code here</pre>"},
		{"fenced-with-lang", "```go\nfunc x() {}\n```", `<pre><code class="language-go">func x() {}</code></pre>`},
		{"fenced-escapes-and-no-md", "```python\nx = 1 < 2 & **stay**\n```",
			`<pre><code class="language-python">x = 1 &lt; 2 &amp; **stay**</code></pre>`},
		{"fenced-multiline", "```\nline1\nline2\n```", "<pre>line1\nline2</pre>"},

		// --- Links ---
		{"link", "[label](http://x)", `<a href="http://x">label</a>`},
		{"link-in-sentence", "see [docs](https://e.com) now", `see <a href="https://e.com">docs</a> now`},
		{"link-url-amp-escaped", "[q](https://x/a?b=1&c=2)", `<a href="https://x/a?b=1&amp;c=2">q</a>`},

		// --- Blockquote ---
		{"blockquote-single", "> a quoted line", "<blockquote>a quoted line</blockquote>"},
		{"blockquote-multi-run", "> line one\n> line two",
			"<blockquote>line one\nline two</blockquote>"},
		{"blockquote-with-text-after", "> quoted\nplain after",
			"<blockquote>quoted</blockquote>\nplain after"},

		// --- Lists (rendered as plain bullet text — Telegram has no list tag) ---
		{"unordered-dash", "- first\n- second", "• first\n• second"},
		{"unordered-star-plus", "* a\n+ b", "• a\n• b"},
		{"ordered", "1. one\n2. two", "1. one\n2. two"},
		{"ordered-multi-digit", "9. nine\n10. ten", "9. nine\n10. ten"},
		{"ordered-paren", "1) one\n2) two", "1. one\n2. two"},

		// --- Nesting ---
		{"bold-in-list-item", "- a **bold** item", "• a <b>bold</b> item"},
		{"link-in-bold", "**see [here](http://y)**", `<b>see <a href="http://y">here</a></b>`},
		{"italic-in-blockquote", "> a *quoted* word", "<blockquote>a <i>quoted</i> word</blockquote>"},

		// --- Intraword underscores stay literal (CommonMark rule) ---
		{"underscores-stay", "mcp__plugin_c3_c3__attach", "mcp__plugin_c3_c3__attach"},
		{"snake-case-stays", "snake_case_var here", "snake_case_var here"},

		// --- Degenerate / unclosed markers pass through ---
		{"multiline-bold-not-spanned", "**not\nbold**", "**not\nbold**"},
		{"unclosed-bold-stays", "lone ** asterisks", "lone ** asterisks"},
		{"unclosed-backtick-stays", "lone ` tick", "lone ` tick"},
		{"arithmetic-stars-stay", "a * b * c", "a * b * c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mdToTelegramHTML(tc.in)
			if got != tc.want {
				t.Errorf("mdToTelegramHTML(%q):\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}
