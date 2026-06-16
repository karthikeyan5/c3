package telegram

import (
	"strings"
)

// maxMonoTableWidth is the column-width budget (in display runes) for a rendered
// monospace table. It exists for guidance/awareness only: per the Q-TABLE-1
// maintainer decision (RENDER-ANYWAY), a table whose total rendered width
// EXCEEDS this budget is still rendered as-is — Telegram won't reject it, and
// wrapping vs. horizontal scroll becomes the client's behavior (Android scrolls;
// desktop/web wrap). We do NOT auto-transpose or convert to an image.
//
// Living here (the telegram package) keeps the `4096`-adjacent width-budget
// concept out of core: the value isn't 4096, but the principle is the same —
// rendering width is a Telegram-channel detail, not a core capability fact.
const maxMonoTableWidth = 80

// detectTable recognizes a GFM pipe-table block starting at lines[i] and returns
// its parsed rows (header row first, then body rows), the index just past the
// block, and ok=true. It is the Telegram-side detector that drives
// renderTableMono; the chunk.go atomicity grouping uses its own pure mirror of
// the same shape test (no cross-package coupling).
//
// GFM shape recognized:
//   - a HEADER line that contains at least one '|';
//   - immediately followed by a DELIMITER line of the form
//     `|? :?-+:? (| :?-+:?)+ |?` (a run of dash cells, optional leading/trailing
//     pipe, optional `:` alignment markers) — the delimiter alone is what
//     distinguishes a real table from prose that merely contains a pipe;
//   - then zero or more BODY rows (any line containing '|'), until a line with no
//     '|' (or end of input) ends the block.
//
// Cells are returned trimmed, with the optional leading/trailing pipe stripped.
// The delimiter row itself is NOT returned as data (renderTableMono draws its own
// ASCII rule under the header).
func detectTable(lines []string, i int) (rows [][]string, end int, ok bool) {
	// Need at least a header line and a delimiter line.
	if i+1 >= len(lines) {
		return nil, 0, false
	}
	header := lines[i]
	if !strings.Contains(header, "|") {
		return nil, 0, false
	}
	if !isTableDelimiter(lines[i+1]) {
		return nil, 0, false
	}

	headerCells := splitTableRow(header)
	// The header column count fixes the table's column count; the delimiter must
	// describe at least one column and the header at least one cell.
	if len(headerCells) == 0 {
		return nil, 0, false
	}
	// GFM requires the delimiter row to have the SAME number of cells as the
	// header. Without this check, prose containing a pipe followed by a bare
	// thematic break (`some prose | aside` then `---`) is mis-detected as a
	// table and rendered as a corrupted <pre>. Split the delimiter the SAME way
	// the header is split so the counts are comparable.
	if len(splitTableRow(lines[i+1])) != len(headerCells) {
		return nil, 0, false
	}

	rows = append(rows, headerCells)

	j := i + 2
	for j < len(lines) {
		// A body row must contain a pipe; the first line without one ends the table.
		if !strings.Contains(lines[j], "|") {
			break
		}
		// A second delimiter-shaped line is not a body row — stop before it so a
		// following table isn't swallowed. (Rare, but keeps the block well-formed.)
		if isTableDelimiter(lines[j]) {
			break
		}
		rows = append(rows, splitTableRow(lines[j]))
		j++
	}

	return rows, j, true
}

// isTableDelimiter reports whether line is a GFM table delimiter row: a run of
// dash cells separated by pipes, each cell optionally carrying `:` alignment
// markers, with an optional leading/trailing pipe. There must be at least one
// cell. This is the load-bearing signal that a `|`-containing line is a table
// and not prose.
func isTableDelimiter(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	// Strip one optional leading/trailing pipe so the cell scan is uniform.
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	cells := strings.Split(t, "|")
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if !isDelimiterCell(c) {
			return false
		}
	}
	return true
}

// isDelimiterCell reports whether c is a single GFM delimiter cell: optional
// surrounding whitespace, an optional leading `:`, one or more `-`, an optional
// trailing `:` — e.g. `---`, `:--`, `--:`, `:-:`.
func isDelimiterCell(c string) bool {
	c = strings.TrimSpace(c)
	c = strings.TrimPrefix(c, ":")
	c = strings.TrimSuffix(c, ":")
	if c == "" {
		return false
	}
	for _, r := range c {
		if r != '-' {
			return false
		}
	}
	return true
}

// splitTableRow splits a GFM table row into trimmed cells, stripping one optional
// leading/trailing pipe so `| a | b |` and `a | b` both yield ["a","b"].
func splitTableRow(line string) []string {
	t := strings.TrimSpace(line)
	t = strings.TrimPrefix(t, "|")
	t = strings.TrimSuffix(t, "|")
	parts := strings.Split(t, "|")
	cells := make([]string, len(parts))
	for k, p := range parts {
		cells[k] = strings.TrimSpace(p)
	}
	return cells
}

// renderTableMono renders parsed table rows (header first) as an aligned
// fixed-width ASCII block wrapped in <pre>. Columns are space-padded to each
// column's maximum display width (measured in runes), joined with " | ", with an
// ASCII `-+-` rule drawn under the header. Box-drawing characters are
// deliberately NOT used: they render at inconsistent widths in many Telegram
// fonts, which breaks the alignment this function exists to provide.
//
// Each cell's content is HTML-escaped (escapeText) so `< > &` can't break the
// <pre> parse; the padding spaces and the " | " / "-+-" separators are literal
// ASCII (no escaping needed).
//
// CJK / double-width caveat: display width is counted in RUNES, so a CJK cell
// (rendered double-width by the client) will under-pad and not align perfectly.
// This is an accepted, documented limitation.
//
// Per Q-TABLE-1 (RENDER-ANYWAY): a table whose rendered line width exceeds
// maxMonoTableWidth is still emitted unchanged — no transpose, no image.
func renderTableMono(rows [][]string) string {
	if len(rows) == 0 {
		return "<pre></pre>"
	}

	// Column count = the widest row (defensive against ragged rows).
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	if cols == 0 {
		return "<pre></pre>"
	}

	// Per-column max display width (runes), across every row.
	widths := make([]int, cols)
	for _, r := range rows {
		for c := 0; c < cols; c++ {
			cell := ""
			if c < len(r) {
				cell = r[c]
			}
			if w := displayWidth(cell); w > widths[c] {
				widths[c] = w
			}
		}
	}

	const colSep = " | "
	var b strings.Builder
	b.WriteString("<pre>")

	// Header row.
	b.WriteString(padRow(rows[0], widths, colSep))

	// ASCII rule under the header: each column is `-`*width, columns joined by
	// "-+-" so the verticals of the " | " separators line up.
	b.WriteString("\n")
	for c := 0; c < cols; c++ {
		if c > 0 {
			b.WriteString("-+-")
		}
		b.WriteString(strings.Repeat("-", widths[c]))
	}

	// Body rows.
	for _, r := range rows[1:] {
		b.WriteString("\n")
		b.WriteString(padRow(r, widths, colSep))
	}

	b.WriteString("</pre>")
	return b.String()
}

// padRow renders one row: each cell HTML-escaped and right-padded with spaces to
// its column width, cells joined by sep. Padding is computed from the UNESCAPED
// display width (so escaping doesn't throw off alignment), then appended as plain
// spaces after the escaped content.
func padRow(row []string, widths []int, sep string) string {
	var b strings.Builder
	for c := 0; c < len(widths); c++ {
		if c > 0 {
			b.WriteString(sep)
		}
		cell := ""
		if c < len(row) {
			cell = row[c]
		}
		b.WriteString(escapeText(cell))
		if pad := widths[c] - displayWidth(cell); pad > 0 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	return b.String()
}

// displayWidth returns a cell's display width as a rune count. CJK double-width
// is intentionally NOT special-cased (documented caveat on renderTableMono).
func displayWidth(s string) int {
	return len([]rune(s))
}
