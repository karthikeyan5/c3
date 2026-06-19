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
		// Italic wrapping bold: the single `*` must not close at the inner `**`.
		{"italic-wrapping-bold-stars", "*a **b** c*", "<i>a <b>b</b> c</i>"},
		{"italic-wrapping-bold-unders", "_a __b__ c_", "<i>a <b>b</b> c</i>"},

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
		// URL with balanced parens must not truncate at the first ')'.
		{"link-url-balanced-parens", "[Foo](https://en.wikipedia.org/wiki/Foo_(bar))",
			`<a href="https://en.wikipedia.org/wiki/Foo_(bar)">Foo</a>`},
		{"link-url-balanced-parens-in-sentence", "see [Foo](https://en.wikipedia.org/wiki/Foo_(bar)) here",
			`see <a href="https://en.wikipedia.org/wiki/Foo_(bar)">Foo</a> here`},

		// --- Blockquote ---
		{"blockquote-single", "> a quoted line", "<blockquote>a quoted line</blockquote>"},
		{"blockquote-multi-run", "> line one\n> line two",
			"<blockquote>line one\nline two</blockquote>"},
		{"blockquote-with-text-after", "> quoted\nplain after",
			"<blockquote>quoted</blockquote>\nplain after"},

		// --- Expandable ("Show more") blockquote: last quoted line ends with a
		// bare "||" terminator → <blockquote expandable>, terminator stripped. ---
		{"blockquote-expandable-single", "> a long quote||",
			"<blockquote expandable>a long quote</blockquote>"},
		{"blockquote-expandable-multi", "> line one\n> line two||",
			"<blockquote expandable>line one\nline two</blockquote>"},
		{"blockquote-expandable-bare-marker-line", "> line one\n> line two\n> ||",
			"<blockquote expandable>line one\nline two\n</blockquote>"},
		// A genuine inline spoiler inside a quote ends with "||" but is a PAIRED
		// ||x|| — it must render as a spoiler, NOT trigger expandable.
		{"blockquote-with-spoiler-not-expandable", "> the answer is ||42||",
			`<blockquote>the answer is <span class="tg-spoiler">42</span></blockquote>`},
		// Expandable quote whose last line ALSO contains a real spoiler: the
		// unpaired trailing "||" is the terminator; the inner spoiler still works.
		{"blockquote-expandable-with-inner-spoiler", "> see ||secret|| now||",
			`<blockquote expandable>see <span class="tg-spoiler">secret</span> now</blockquote>`},

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

		// --- Same-type emphasis nesting (mixed spellings) must COLLAPSE ---
		// Telegram's HTML parser rejects an entity nested inside another entity of
		// the SAME type (a <b> inside a <b>) with a 400 that strips ALL formatting.
		// When an agent mixes the two spellings of one level we render the inner
		// span WITHOUT re-wrapping (it is already bold/italic).
		{"same-type-bold-collapse", "**a __b__ c**", "<b>a b c</b>"},
		{"same-type-italic-collapse", "*a _b_ c*", "<i>a b c</i>"},
		{"same-type-bold-collapse-unders-outer", "__x **y** z__", "<b>x y z</b>"},
		{"same-type-italic-collapse-unders-outer", "_x *y* z_", "<i>x y z</i>"},

		// --- Different-type emphasis nesting must be PRESERVED ---
		// Telegram ALLOWS <b><i>…</i></b>; only same-type nesting is illegal.
		{"diff-type-bold-italic", "**bold _it_ end**", "<b>bold <i>it</i> end</b>"},
		{"diff-type-italic-bold", "_it **bo** end_", "<i>it <b>bo</b> end</i>"},

		// --- Bold+italic triple marker → <b><i>…</i></b> ---
		{"triple-stars", "***text***", "<b><i>text</i></b>"},
		{"triple-unders", "___text___", "<b><i>text</i></b>"},

		// --- Same-type collapse must hold INSIDE other constructs ---
		{"same-type-in-list-item", "- **a __b__**", "• <b>a b</b>"},
		{"same-type-in-blockquote", "> **a __b__**", "<blockquote><b>a b</b></blockquote>"},
		{"same-type-in-link-label", "[**a __b__**](https://x)", `<a href="https://x"><b>a b</b></a>`},

		// --- Intraword underscores stay literal (CommonMark rule) ---
		{"underscores-stay", "mcp__plugin_c3_c3__attach", "mcp__plugin_c3_c3__attach"},
		{"snake-case-stays", "snake_case_var here", "snake_case_var here"},

		// --- Degenerate / unclosed markers pass through ---
		{"multiline-bold-not-spanned", "**not\nbold**", "**not\nbold**"},
		{"unclosed-bold-stays", "lone ** asterisks", "lone ** asterisks"},
		{"unclosed-backtick-stays", "lone ` tick", "lone ` tick"},
		{"arithmetic-stars-stay", "a * b * c", "a * b * c"},

		// --- GFM pipe tables → aligned monospace <pre> ---
		// Basic table: cells padded to per-column max width, " | " separator, an
		// ASCII "-+-" rule under the header (never box-drawing chars).
		{"table-basic", "| a | bb |\n|---|----|\n| 1 | 2 |",
			"<pre>a | bb\n--+---\n1 | 2 </pre>"},
		// No leading/trailing pipes is still a table (delimiter row is the signal).
		{"table-no-edge-pipes", "a | bb\n--- | ----\n1 | 2",
			"<pre>a | bb\n--+---\n1 | 2 </pre>"},
		// Alignment colons in the delimiter are accepted; columns still align.
		{"table-aligned-colons", "| h1 | h2 |\n|:--|--:|\n| x | yy |",
			"<pre>h1 | h2\n---+---\nx  | yy</pre>"},
		// Cell content is HTML-escaped inside <pre>.
		{"table-escapes-cell", "| a | b |\n|---|---|\n| <x> | & |",
			"<pre>a   | b\n----+--\n&lt;x&gt; | &amp;</pre>"},
		// A lone pipe in prose (no delimiter row) is NOT a table — stays literal.
		{"not-a-table-prose-pipe", "a | b is just text", "a | b is just text"},
		// A pipe header with no delimiter row underneath is NOT a table.
		{"not-a-table-no-delimiter", "| a | b |\n| 1 | 2 |", "| a | b |\n| 1 | 2 |"},
		// A prose line containing a pipe followed by a bare `---` thematic break is
		// NOT a table: the delimiter row's cell count (1) must equal the header's
		// (2). Without the cell-count check this rendered as a corrupted <pre>.
		{"not-a-table-prose-pipe-then-rule", "some prose | aside\n---\nmore text",
			"some prose | aside\n---\nmore text"},
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

// assertNoSameTypeNesting scans rendered Telegram HTML's tag stream with a stack
// and fails if a <b> is opened while a <b> is already open, or a <i> while a <i>
// is already open. Telegram rejects same-type nested entities at ANY depth with a
// 400 that strips all formatting, so this is the durable regression guard the
// audit said was missing. (Different-type nesting — <b><i> — is legal and must
// pass.) Only the emphasis tags <b>/<i> are tracked; other tags are ignored.
func assertNoSameTypeNesting(t *testing.T, in, htmlOut string) {
	t.Helper()
	var stack []string
	runes := []rune(htmlOut)
	for i := 0; i < len(runes); i++ {
		if runes[i] != '<' {
			continue
		}
		// Read the tag up to '>'.
		j := i + 1
		for j < len(runes) && runes[j] != '>' {
			j++
		}
		if j >= len(runes) {
			break
		}
		tag := string(runes[i+1 : j])
		i = j
		closing := false
		if len(tag) > 0 && tag[0] == '/' {
			closing = true
			tag = tag[1:]
		}
		// Reduce to the tag name (first token; e.g. "blockquote expandable").
		name := tag
		if sp := indexByte(name, ' '); sp >= 0 {
			name = name[:sp]
		}
		if name != "b" && name != "i" {
			continue // only emphasis tags can collide same-type
		}
		if closing {
			if len(stack) > 0 && stack[len(stack)-1] == name {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		for _, open := range stack {
			if open == name {
				t.Errorf("same-type <%s> nesting in output for input %q:\n  %q", name, in, htmlOut)
				return
			}
		}
		stack = append(stack, name)
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// TestNoSameTypeNesting runs the property guard over every mixed-spelling input,
// each wrapped in plain text, a list item, a blockquote, and a link label — the
// four places renderInline is reused.
func TestNoSameTypeNesting(t *testing.T) {
	mixed := []string{
		"**a __b__ c**",
		"*a _b_ c*",
		"__x **y** z__",
		"_x *y* z_",
		"**bold _it_ end**",
		"_it **bo** end_",
		"***text***",
		"___text___",
		"**a __b__ c** and *d _e_ f*",
	}
	for _, m := range mixed {
		wrappers := map[string]string{
			"plain":      m,
			"list-item":  "- " + m,
			"blockquote": "> " + m,
			"link-label": "[" + m + "](https://x)",
		}
		for wname, in := range wrappers {
			t.Run(wname+":"+m, func(t *testing.T) {
				assertNoSameTypeNesting(t, in, mdToTelegramHTML(in))
			})
		}
	}
}
