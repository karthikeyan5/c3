package telegram

import "testing"

func TestMdToTelegramHTML(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world", "hello world"},
		{"bold", "say **hi** there", "say <b>hi</b> there"},
		{"two-bolds", "**a** then **b**", "<b>a</b> then <b>b</b>"},
		{"bold-with-html-chars", "**a < b**", "<b>a &lt; b</b>"},
		{"inline-code", "use `c3-broker status`", "use <code>c3-broker status</code>"},
		{"code-with-html", "see `<b>` tag", "see <code>&lt;b&gt;</code> tag"},
		{"underscores-stay", "mcp__plugin_c3_c3__attach", "mcp__plugin_c3_c3__attach"},
		{"links-stay", "[label](http://x)", "[label](http://x)"},
		{"angle-escape", "a < b > c & d", "a &lt; b &gt; c &amp; d"},
		{"multiline-bold-not-spanned", "**not\nbold**", "**not\nbold**"},
		{"unclosed-bold-stays", "lone ** asterisks", "lone ** asterisks"},
		{"unclosed-backtick-stays", "lone ` tick", "lone ` tick"},
		{"triple-bare", "```\ncode here\n```", "<pre>\ncode here\n</pre>"},
		{"triple-with-lang", "```go\nfunc x() {}\n```", `<pre><code class="language-go">func x() {}
</code></pre>`},
		{"bold-inside-code-not-bolded", "`**not bold**`", "<code>**not bold**</code>"},
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
