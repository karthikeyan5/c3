package telegram

import (
	"html"
	"strings"
)

// mdToTelegramHTML converts standard (common) markdown to Telegram's HTML
// parse_mode. Telegram doesn't render raw markdown; without conversion,
// `**bold**` shows as literal asterisks (2026-05-09 photo report).
//
// Telegram HTML accepts only a fixed tag whitelist — any other tag triggers a
// 400 "unsupported start tag" error. We therefore map markdown onto ONLY these
// allowed tags:
//
//   - Bold:          **x** | __x__              → <b>x</b>
//   - Italic:        *x*   | _x_                → <i>x</i>
//   - Strikethrough: ~~x~~                      → <s>x</s>
//   - Spoiler:       ||x||                      → <span class="tg-spoiler">x</span>
//   - Inline code:   `x`                        → <code>x</code>
//   - Fenced code:   ```lang\n...\n```          → <pre><code class="language-lang">...</code></pre>
//                    ```\n...\n```              → <pre>...</pre>
//   - Link:          [label](url)               → <a href="url">label</a>
//   - Blockquote:    a run of "> " lines        → <blockquote>...</blockquote>
//   - Lists:         "- "/"* "/"+ "/"N. " items → bullet TEXT ("• item"), since
//                    Telegram HTML has NO list tag. Ordered lists keep their
//                    numbers; unordered lists get a "• " prefix.
//
// There is no standard markdown for underline, so underline is not emitted.
//
// Escaping: the three HTML-significant bytes `< > &` in any text content become
// `&lt; &gt; &amp;` so literal angle brackets in user/agent text can't break
// parsing. Inside code/pre, content is treated as LITERAL — markdown is not
// interpreted there, only `< > &` are escaped. Inside <a href="...">, the URL is
// additionally escaped for `"` so it can't terminate the attribute early.
//
// Tag-balance guarantee: every emitted tag opens and closes within this
// function. Long-message chunking happens BEFORE this conversion runs (in
// SendReply), so chunk boundaries can't split a tag pair.
//
// The function signature is unchanged (per-chunk converter); SendReply keeps
// calling it as before.
func mdToTelegramHTML(s string) string {
	// Block-level pass: split into lines and group fenced code blocks,
	// blockquote runs, and list items. Inline conversion happens on the
	// non-code text of each line.
	lines := strings.Split(s, "\n")
	var out []string

	i := 0
	for i < len(lines) {
		line := lines[i]

		// Fenced code block: a line whose trimmed-left content starts with ```.
		if fence, lang, ok := openFence(line); ok {
			var body []string
			j := i + 1
			closed := false
			for j < len(lines) {
				if isFenceClose(lines[j], fence) {
					closed = true
					break
				}
				body = append(body, lines[j])
				j++
			}
			if closed {
				content := strings.Join(body, "\n")
				if lang != "" {
					out = append(out, `<pre><code class="language-`+html.EscapeString(lang)+`">`+html.EscapeString(content)+`</code></pre>`)
				} else {
					out = append(out, `<pre>`+html.EscapeString(content)+`</pre>`)
				}
				i = j + 1
				continue
			}
			// Unclosed fence: treat the opener line as ordinary text (fall
			// through to inline handling below).
		}

		// Blockquote run: consecutive lines beginning with "> " (or exactly ">").
		if isBlockquote(line) {
			var quoted []string
			j := i
			for j < len(lines) && isBlockquote(lines[j]) {
				quoted = append(quoted, renderInline(stripBlockquote(lines[j])))
				j++
			}
			out = append(out, "<blockquote>"+strings.Join(quoted, "\n")+"</blockquote>")
			i = j
			continue
		}

		// List item (unordered or ordered): render as a plain bullet line.
		if marker, rest, ok := listItem(line); ok {
			out = append(out, marker+renderInline(rest))
			i++
			continue
		}

		// Ordinary line: inline conversion only.
		out = append(out, renderInline(line))
		i++
	}

	return strings.Join(out, "\n")
}

// openFence reports whether line opens a fenced code block. It returns the fence
// token (```` ``` ```` or `~~~`) and the optional language hint.
func openFence(line string) (fence, lang string, ok bool) {
	t := strings.TrimLeft(line, " ")
	for _, f := range []string{"```", "~~~"} {
		if strings.HasPrefix(t, f) {
			info := strings.TrimSpace(t[len(f):])
			// An info string with no internal whitespace is a language hint;
			// anything with spaces is ignored (not a valid single lang token).
			if info != "" && !strings.ContainsAny(info, " \t") {
				return f, info, true
			}
			return f, "", true
		}
	}
	return "", "", false
}

// isFenceClose reports whether line closes a fence opened with token fence.
func isFenceClose(line, fence string) bool {
	t := strings.TrimSpace(line)
	return t == fence || (strings.HasPrefix(t, fence) && strings.TrimSpace(t[len(fence):]) == "")
}

// isBlockquote reports whether line is part of a blockquote run.
func isBlockquote(line string) bool {
	return strings.HasPrefix(line, "> ") || line == ">"
}

// stripBlockquote removes the leading "> " (or ">") marker.
func stripBlockquote(line string) string {
	if line == ">" {
		return ""
	}
	return strings.TrimPrefix(line, "> ")
}

// listItem reports whether line is a markdown list item and returns the rendered
// marker prefix plus the item text. Unordered markers (-, *, +) become "• ";
// ordered markers (N. or N)) keep their number as "N. ".
func listItem(line string) (marker, rest string, ok bool) {
	// Preserve leading indentation so nested lists keep some structure.
	indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	t := line[len(indent):]

	// Unordered: "- ", "* ", "+ " (require the trailing space so "*x*" italic
	// and "**bold**" are not mistaken for a bullet).
	if len(t) >= 2 && (t[0] == '-' || t[0] == '*' || t[0] == '+') && t[1] == ' ' {
		return indent + "• ", strings.TrimSpace(t[2:]), true
	}

	// Ordered: a run of digits followed by "." or ")" then a space.
	d := 0
	for d < len(t) && t[d] >= '0' && t[d] <= '9' {
		d++
	}
	if d > 0 && d < len(t) && (t[d] == '.' || t[d] == ')') {
		after := t[d+1:]
		if strings.HasPrefix(after, " ") {
			return indent + t[:d] + ". ", strings.TrimSpace(after), true
		}
	}

	return "", "", false
}

// renderInline converts inline markdown within a single line of text to the
// allowed Telegram HTML tags, escaping `< > &` in all non-tag text. Inline code
// spans are extracted first (their content is literal). Links are recognized,
// then emphasis markers (bold/italic/strike/spoiler).
func renderInline(s string) string {
	var b strings.Builder
	runes := []rune(s)
	i := 0
	n := len(runes)

	for i < n {
		c := runes[i]

		// Inline code: `...` — content is literal (no markdown inside).
		if c == '`' {
			if end := indexRune(runes, '`', i+1); end > i {
				b.WriteString("<code>")
				b.WriteString(escapeText(string(runes[i+1 : end])))
				b.WriteString("</code>")
				i = end + 1
				continue
			}
		}

		// Link: [label](url)
		if c == '[' {
			if label, url, next, ok := parseLink(runes, i); ok {
				b.WriteString(`<a href="`)
				b.WriteString(escapeURL(url))
				b.WriteString(`">`)
				// The label may itself contain emphasis; render it recursively.
				b.WriteString(renderInline(label))
				b.WriteString("</a>")
				i = next
				continue
			}
		}

		// Spoiler: ||...||
		if c == '|' && i+1 < n && runes[i+1] == '|' {
			if inner, next, ok := matchDelim(runes, i, "||", false); ok {
				b.WriteString(`<span class="tg-spoiler">`)
				b.WriteString(renderInline(inner))
				b.WriteString(`</span>`)
				i = next
				continue
			}
		}

		// Strikethrough: ~~...~~
		if c == '~' && i+1 < n && runes[i+1] == '~' {
			if inner, next, ok := matchDelim(runes, i, "~~", false); ok {
				b.WriteString("<s>")
				b.WriteString(renderInline(inner))
				b.WriteString("</s>")
				i = next
				continue
			}
		}

		// Bold: **...** or __...__
		if c == '*' && i+1 < n && runes[i+1] == '*' {
			if inner, next, ok := matchDelim(runes, i, "**", false); ok {
				b.WriteString("<b>")
				b.WriteString(renderInline(inner))
				b.WriteString("</b>")
				i = next
				continue
			}
		}
		if c == '_' && i+1 < n && runes[i+1] == '_' {
			if inner, next, ok := matchDelim(runes, i, "__", true); ok {
				b.WriteString("<b>")
				b.WriteString(renderInline(inner))
				b.WriteString("</b>")
				i = next
				continue
			}
		}

		// Italic: *...* or _..._  (single delimiter; the double-cases above
		// already consumed ** and __, so a bare single marker remains).
		if c == '*' {
			if inner, next, ok := matchDelim(runes, i, "*", false); ok {
				b.WriteString("<i>")
				b.WriteString(renderInline(inner))
				b.WriteString("</i>")
				i = next
				continue
			}
		}
		if c == '_' {
			if inner, next, ok := matchDelim(runes, i, "_", true); ok {
				b.WriteString("<i>")
				b.WriteString(renderInline(inner))
				b.WriteString("</i>")
				i = next
				continue
			}
		}

		// Default: escape one rune of plain text.
		b.WriteString(escapeRune(c))
		i++
	}

	return b.String()
}

// matchDelim, starting at the opening delimiter delim at index i in runes,
// finds the next non-empty matching closing delim on the SAME line and returns
// the inner text, the index just past the closing delim, and ok.
//
// Flanking rules (a simplified CommonMark subset) keep stray delimiters literal:
//   - The opening delim must not be immediately followed by whitespace, and the
//     closing delim must not be immediately preceded by whitespace. So "a * b *"
//     (arithmetic) and a bare "**" pass through as literal.
//   - When wordBoundary is true (underscore-based emphasis), the open delim must
//     not be preceded by an alphanumeric and the close must not be followed by
//     one. This is CommonMark's intraword-underscore rule and keeps strings like
//     "mcp__plugin_c3_c3__attach" literal.
func matchDelim(runes []rune, i int, delim string, wordBoundary bool) (inner string, next int, ok bool) {
	dl := len([]rune(delim))
	start := i + dl
	if start >= len(runes) {
		return "", 0, false
	}
	// Open delim must not be followed by whitespace.
	if isSpace(runes[start]) {
		return "", 0, false
	}
	delimRune := []rune(delim)[0]
	// Underscore emphasis: open must not be preceded by an alphanumeric, nor sit
	// mid delimiter-run (preceded by the same rune) — this keeps strings like
	// "mcp__plugin_c3_c3__attach" literal.
	if wordBoundary && i > 0 && (isAlnum(runes[i-1]) || runes[i-1] == delimRune) {
		return "", 0, false
	}
	// Search for the closing delimiter.
	for j := start; j+dl <= len(runes); j++ {
		if string(runes[j:j+dl]) == delim {
			candidate := runes[start:j]
			if len(candidate) == 0 {
				return "", 0, false // empty span, not emphasis
			}
			// Close delim must not be preceded by whitespace.
			if isSpace(candidate[len(candidate)-1]) {
				continue
			}
			// Underscore emphasis: close must not be followed by an alphanumeric
			// nor sit mid delimiter-run (followed by the same rune).
			if wordBoundary && j+dl < len(runes) && (isAlnum(runes[j+dl]) || runes[j+dl] == delimRune) {
				continue
			}
			return string(candidate), j + dl, true
		}
	}
	return "", 0, false
}

// isSpace reports whether r is an ASCII whitespace rune.
func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// isAlnum reports whether r is an ASCII alphanumeric rune (used for the
// underscore intraword-emphasis rule).
func isAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// parseLink parses [label](url) starting at the '[' at index i. Returns the
// label, url, the index just past the closing ')', and ok.
func parseLink(runes []rune, i int) (label, url string, next int, ok bool) {
	// label
	close := indexRune(runes, ']', i+1)
	if close < 0 {
		return "", "", 0, false
	}
	// next char must be '('
	if close+1 >= len(runes) || runes[close+1] != '(' {
		return "", "", 0, false
	}
	paren := indexRune(runes, ')', close+2)
	if paren < 0 {
		return "", "", 0, false
	}
	label = string(runes[i+1 : close])
	url = strings.TrimSpace(string(runes[close+2 : paren]))
	if url == "" {
		return "", "", 0, false
	}
	return label, url, paren + 1, true
}

// indexRune returns the index of the first occurrence of r in runes at or after
// from, or -1.
func indexRune(runes []rune, r rune, from int) int {
	for j := from; j < len(runes); j++ {
		if runes[j] == r {
			return j
		}
	}
	return -1
}

// escapeText escapes `< > &` in literal text content (e.g. code spans).
func escapeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteString(escapeRune(r))
	}
	return b.String()
}

// escapeRune escapes a single rune for HTML text content.
func escapeRune(r rune) string {
	switch r {
	case '<':
		return "&lt;"
	case '>':
		return "&gt;"
	case '&':
		return "&amp;"
	default:
		return string(r)
	}
}

// escapeURL escapes a URL for use inside an href="..." attribute. In addition
// to the text-content set it escapes `"` so the attribute can't be terminated
// early.
func escapeURL(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
