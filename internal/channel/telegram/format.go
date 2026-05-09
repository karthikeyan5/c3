package telegram

import (
	"html"
	"strings"
)

// mdToTelegramHTML converts a subset of standard markdown to Telegram's HTML
// parse_mode. Telegram doesn't render raw markdown; without conversion,
// `**bold**` shows as literal asterisks (Karthi 2026-05-09 photo report).
//
// Conversions handled (well enough to render LLM-generated text cleanly):
//
//   - ```` ``` `` ``` ` `` → <pre>...</pre>  (with optional `lang` hint becoming
//     <pre><code class="language-lang">...</code></pre>)
//   - ` `code` ` → <code>...</code>
//   - `**bold**`  → <b>...</b>
//
// Everything else is HTML-escaped and passed through as plain text — this
// includes `_underscores_`, `[links](urls)`, and other markdown forms we
// don't translate, so they appear as the literal characters the user typed.
// The 90% common cases (bold + code) are the ones that actually break in
// raw text.
//
// HTML escaping is applied INSIDE every span (so e.g. `code with <` becomes
// `<code>code with &lt;</code>`). Outside any span, every byte is escaped
// via html.EscapeString.
//
// Tag-balance guarantee: every <pre>, <code>, <b> opens and closes within
// this function. Long-message chunking happens BEFORE this conversion runs
// (in SendReply) so chunk boundaries can't split a tag pair.
func mdToTelegramHTML(s string) string {
	var b strings.Builder
	i := 0
	n := len(s)
	for i < n {
		// Triple-backtick code block.
		if i+3 <= n && s[i:i+3] == "```" {
			rest := s[i+3:]
			end := strings.Index(rest, "```")
			if end >= 0 {
				body := rest[:end]
				lang := ""
				if nl := strings.IndexByte(body, '\n'); nl > 0 {
					first := strings.TrimSpace(body[:nl])
					if first != "" && !strings.ContainsAny(first, " \t") {
						lang = first
						body = body[nl+1:]
					}
				}
				if lang != "" {
					b.WriteString(`<pre><code class="language-`)
					b.WriteString(html.EscapeString(lang))
					b.WriteString(`">`)
					b.WriteString(html.EscapeString(body))
					b.WriteString("</code></pre>")
				} else {
					b.WriteString("<pre>")
					b.WriteString(html.EscapeString(body))
					b.WriteString("</pre>")
				}
				i += 3 + end + 3
				continue
			}
		}
		// Inline backtick code.
		if s[i] == '`' {
			end := strings.IndexByte(s[i+1:], '`')
			if end >= 0 {
				b.WriteString("<code>")
				b.WriteString(html.EscapeString(s[i+1 : i+1+end]))
				b.WriteString("</code>")
				i += 1 + end + 1
				continue
			}
		}
		// Bold **...**. Match minimally — first `**` after the opener.
		if i+2 <= n && s[i:i+2] == "**" {
			end := strings.Index(s[i+2:], "**")
			// Only treat as bold if the inner text is non-empty and contains
			// no newline (avoid swallowing across paragraphs).
			if end > 0 && !strings.ContainsRune(s[i+2:i+2+end], '\n') {
				b.WriteString("<b>")
				b.WriteString(html.EscapeString(s[i+2 : i+2+end]))
				b.WriteString("</b>")
				i += 2 + end + 2
				continue
			}
		}
		// Default: escape this rune. Strings are byte-indexed; html.EscapeString
		// handles single bytes fine (multi-byte UTF-8 needs no HTML escaping in
		// the BMP-ish space we're in).
		switch s[i] {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		default:
			b.WriteByte(s[i])
		}
		i++
	}
	return b.String()
}
