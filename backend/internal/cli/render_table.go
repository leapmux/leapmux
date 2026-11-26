package cli

import (
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

// maxCellRunes caps one table cell, so that a long title cannot push the
// other columns off the screen. The "key: value" view prints the whole value.
const maxCellRunes = 60

// tableOptions controls table/TSV rendering (width adaptation, headers, field
// selection, and TTY-only ANSI styling).
type tableOptions struct {
	wide      bool
	noHeader  bool
	fields    []string
	termWidth int
	style     styler
}

// WriteTableWithOptions writes v as an aligned table honoring opts.
func WriteTableWithOptions(w io.Writer, v any, opts TableOptions) error {
	return writeTableOpts(w, toNode(reflect.ValueOf(v)), opts.opts)
}

// selectColumns returns the columns of n filtered and ordered by fields when
// fields is non-empty.
func selectColumns(n node, fields []string) []column {
	cols := n.columns
	if len(cols) == 0 {
		cols = derivedColumns(n)
	}
	if len(fields) == 0 {
		return cols
	}
	byKey := map[string]column{}
	byHeader := map[string]column{}
	for _, c := range cols {
		byKey[strings.ToLower(c.key)] = c
		byHeader[strings.ToLower(c.header)] = c
	}
	out := make([]column, 0, len(fields))
	for _, raw := range fields {
		token := strings.ToLower(strings.TrimSpace(raw))
		if token == "" {
			continue
		}
		if c, ok := byKey[token]; ok {
			out = append(out, c)
		} else if c, ok := byHeader[token]; ok {
			out = append(out, c)
		} else {
			out = append(out, column{key: token, header: strings.ToUpper(token)})
		}
	}
	return out
}

type colStyleKind uint8

const (
	colStylePlain colStyleKind = iota
	colStyleState
	colStyleDim
)

type measuredCell struct {
	text  string
	runes int
}

func indexRowFields(dst map[string]node, row node) {
	clear(dst)
	for _, f := range row.fields {
		lk := strings.ToLower(f.key)
		if _, exists := dst[lk]; !exists {
			dst[lk] = f.value
		}
	}
}

// writeTableOpts writes a list of objects as an aligned table with optional
// header, field selection, terminal-width adaptation, and TTY-only styling.
func writeTableOpts(w io.Writer, n node, opts tableOptions) error {
	cols := selectColumns(n, opts.fields)
	if len(cols) == 0 {
		return nil
	}
	colWidths := make([]int, len(cols))
	var headerWidths []int
	if !opts.noHeader {
		headerWidths = make([]int, len(cols))
		for i, c := range cols {
			hw := utf8.RuneCountInString(c.header)
			headerWidths[i] = hw
			colWidths[i] = hw
		}
	}
	lowerKeys := make([]string, len(cols))
	for i, c := range cols {
		lowerKeys[i] = strings.ToLower(c.key)
	}
	// When terminal width is known (interactive TTY), do not pre-cap cells at
	// maxCellRunes=60; let fitColumnsToWidth dynamically shrink wide columns to
	// match the actual terminal width so wide monitors show full titles/paths.
	uncapped := opts.wide || opts.termWidth > 0
	rawRows := make([][]measuredCell, len(n.items))
	rowIndex := make(map[string]node)
	for rIdx, item := range n.items {
		indexRowFields(rowIndex, item)
		row := make([]measuredCell, len(cols))
		for cIdx, c := range cols {
			val := "-"
			if fv, ok := rowIndex[lowerKeys[cIdx]]; ok {
				val = formatCellValue(fv, c, uncapped)
			}
			rc := utf8.RuneCountInString(val)
			row[cIdx] = measuredCell{text: val, runes: rc}
			colWidths[cIdx] = max(colWidths[cIdx], rc)
		}
		rawRows[rIdx] = row
	}
	// On an interactive TTY (when --wide is not set), shrink the widest prose
	// column so the table fits comfortably inside the terminal width.
	if !opts.wide && opts.termWidth > 0 && len(cols) > 1 {
		colWidths = fitColumnsToWidth(cols, colWidths, headerWidths, opts.termWidth)
	}
	colStyles := make([]colStyleKind, len(cols))
	if opts.style.enabled {
		for i, c := range cols {
			switch {
			case c.state:
				colStyles[i] = colStyleState
			case c.dim || (c.short && i > 0):
				colStyles[i] = colStyleDim
			}
		}
	}
	if !opts.noHeader {
		var sb strings.Builder
		for i, c := range cols {
			if i > 0 {
				sb.WriteString("  ")
			}
			header := c.header
			visRunes := headerWidths[i]
			if visRunes > colWidths[i] {
				header = truncateTo(header, colWidths[i])
				visRunes = colWidths[i]
			}
			if opts.style.enabled {
				sb.WriteString(opts.style.dim(header))
			} else {
				sb.WriteString(header)
			}
			if i < len(cols)-1 {
				if pad := colWidths[i] - visRunes; pad > 0 {
					sb.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		if _, err := fmt.Fprintln(w, sb.String()); err != nil {
			return err
		}
	}
	for _, row := range rawRows {
		var sb strings.Builder
		for i, cell := range row {
			if i > 0 {
				sb.WriteString("  ")
			}
			val := cell.text
			visRunes := cell.runes
			if visRunes > colWidths[i] {
				val = truncateTo(val, colWidths[i])
				visRunes = colWidths[i]
			}
			if opts.style.enabled {
				switch colStyles[i] {
				case colStylePlain:
				case colStyleState:
					val = opts.style.stateBadge(val)
				case colStyleDim:
					val = opts.style.dim(val)
				}
			}
			sb.WriteString(val)
			if i < len(cols)-1 {
				if pad := colWidths[i] - visRunes; pad > 0 {
					sb.WriteString(strings.Repeat(" ", pad))
				}
			}
		}
		if _, err := fmt.Fprintln(w, sb.String()); err != nil {
			return err
		}
	}
	return nil
}

// fitColumnsToWidth shrinks only wide prose columns when total rendered width
// exceeds termWidth, keeping compact ID/state columns and headers intact.
func fitColumnsToWidth(cols []column, widths, headerWidths []int, termWidth int) []int {
	out := append([]int{}, widths...)
	total := (len(out) - 1) * 2
	for _, w := range out {
		total += w
	}
	if total <= termWidth {
		return out
	}
	excess := total - termWidth
	for excess > 0 {
		widestIdx := -1
		widestWidth := 0
		widestGap := 0
		for i, w := range out {
			if cols[i].short {
				continue
			}
			floor := 12
			if i < len(headerWidths) && headerWidths[i] > floor {
				floor = headerWidths[i]
			}
			if w > floor && w > widestWidth {
				widestWidth = w
				widestGap = w - floor
				widestIdx = i
			}
		}
		if widestIdx < 0 {
			break
		}
		shrink := min(excess, widestGap)
		out[widestIdx] -= shrink
		excess -= shrink
	}
	return out
}

func truncateTo(s string, maxRunes int) string {
	if maxRunes <= 0 || utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	if maxRunes == 1 {
		return "…"
	}
	return string([]rune(s)[:maxRunes-1]) + "…"
}

// derivedColumns lists every key that holds a scalar in some row, in the
// order of first appearance, for a list whose type declares no columns.
func derivedColumns(n node) []column {
	seen := map[string]bool{}
	var cols []column
	for _, item := range n.items {
		for _, f := range item.fields {
			if f.value.kind != nodeScalar || seen[f.key] {
				continue
			}
			seen[f.key] = true
			cols = append(cols, column{key: f.key, header: strings.ToUpper(f.key)})
		}
	}
	return cols
}

func formatCellValue(v node, c column, wide bool) string {
	value := SanitizeSingleLine(inlineValue(v))
	if value == "" {
		value = "-"
	}
	if c.short {
		value = ShortID(value)
	}
	if wide {
		return value
	}
	return truncateTo(value, maxCellRunes)
}
