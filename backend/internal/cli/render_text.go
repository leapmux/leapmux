package cli

import (
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var quietLineReplacer = strings.NewReplacer("\r", " ", "\n", " ")

// renderCustomText dispatches OptionsTextRenderer and TextRenderer when v
// implements either interface.
func renderCustomText(w io.Writer, v any, opts tableOptions) (bool, error) {
	if r, ok := v.(OptionsTextRenderer); ok {
		return true, r.RenderTextWithOptions(w, TableOptions{opts: opts})
	}
	if r, ok := v.(TextRenderer); ok {
		return true, r.RenderText(w)
	}
	return false, nil
}

// renderTextOpts writes the text form of v honoring tableOptions.
func renderTextOpts(w io.Writer, v any, opts tableOptions) error {
	if len(opts.fields) == 0 {
		if handled, err := renderCustomText(w, v, opts); handled {
			return err
		}
		if !isNil(v) {
			if hider, ok := v.(DefaultColumnHider); ok {
				opts.hiddenColumns = hider.DefaultHiddenColumns()
			}
		}
	}
	n := toNode(reflect.ValueOf(v))
	switch n.kind {
	case nodeNull:
		return nil
	case nodeScalar:
		_, err := fmt.Fprintln(w, SanitizeTerminalText(n.scalar))
		return err
	case nodeList:
		return writeListOpts(w, n, opts)
	case nodeObject:
		// An object that holds one list and nothing else is that list: the
		// table needs no heading.
		if len(n.fields) == 1 && n.fields[0].value.kind == nodeList {
			return writeListOpts(w, n.fields[0].value, opts)
		}
		if len(opts.fields) > 0 {
			want := fieldSet(opts.fields)
			if listField, ok := primaryListField(n); ok {
				isObjList := listField.value.isObjectList()
				if (isObjList && (!objectHasAllFields(n, want) || nodeListHasAnyField(listField.value, want))) ||
					(!isObjList && !objectHasAnyField(n, want)) {
					return writeListOpts(w, listField.value, opts)
				}
			}
			n = filterObjectFields(n, want)
		}
		return writeObjectOpts(w, n, opts)
	}
	return fmt.Errorf("cli: no text form for node kind %d", n.kind)
}

func objectHasAnyField(n node, want map[string]bool) bool {
	for _, f := range n.fields {
		if want[strings.ToLower(f.key)] {
			return true
		}
	}
	return false
}

func objectHasAllFields(n node, want map[string]bool) bool {
	if len(want) == 0 {
		return false
	}
	present := make(map[string]bool, len(n.fields))
	for _, f := range n.fields {
		present[strings.ToLower(f.key)] = true
	}
	for f := range want {
		if !present[f] {
			return false
		}
	}
	return true
}

func nodeListHasAnyField(list node, want map[string]bool) bool {
	for _, c := range list.columns {
		if want[strings.ToLower(c.key)] || want[strings.ToLower(c.header)] {
			return true
		}
	}
	for _, item := range list.items {
		if item.kind == nodeObject && objectHasAnyField(item, want) {
			return true
		}
	}
	return false
}

// renderTSV writes v as tab-separated values without padding or ANSI colors.
func renderTSV(w io.Writer, v any, opts tableOptions) error {
	n := toNode(reflect.ValueOf(v))
	switch n.kind {
	case nodeNull:
		return nil
	case nodeScalar:
		_, err := fmt.Fprintln(w, SanitizeSingleLine(n.scalar))
		return err
	case nodeList:
		return writeTSVList(w, n, opts)
	case nodeObject:
		if listField, ok := primaryListField(n); ok {
			return writeTSVList(w, listField.value, opts)
		}
		if len(opts.fields) > 0 {
			n = filterObjectFields(n, fieldSet(opts.fields))
		}
		for _, f := range n.fields {
			if _, err := fmt.Fprintf(w, "%s\t%s\n", SanitizeSingleLine(f.key), SanitizeSingleLine(inlineValue(f.value))); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

// renderTSVEvent writes a single stream event as one TSV row.
func renderTSVEvent(w io.Writer, v any, opts tableOptions) error {
	n := toNode(reflect.ValueOf(v))
	if n.kind != nodeObject {
		_, err := fmt.Fprintln(w, SanitizeSingleLine(inlineValue(n)))
		return err
	}
	if len(opts.fields) > 0 {
		n = filterObjectFields(n, fieldSet(opts.fields))
	}
	cells := make([]string, 0, len(n.fields))
	for _, f := range n.fields {
		cells = append(cells, SanitizeSingleLine(inlineValue(f.value)))
	}
	_, err := fmt.Fprintln(w, strings.Join(cells, "\t"))
	return err
}

func writeTSVList(w io.Writer, n node, opts tableOptions) error {
	if isScalarList(n) {
		for _, item := range n.items {
			if _, err := fmt.Fprintln(w, SanitizeSingleLine(item.scalar)); err != nil {
				return err
			}
		}
		return nil
	}
	cols := selectColumns(n, opts.fields)
	if len(cols) == 0 {
		return nil
	}
	if !opts.noHeader {
		headers := make([]string, len(cols))
		for i, c := range cols {
			headers[i] = c.header
		}
		if _, err := fmt.Fprintln(w, strings.Join(headers, "\t")); err != nil {
			return err
		}
	}
	lowerKeys := make([]string, len(cols))
	for i, c := range cols {
		lowerKeys[i] = strings.ToLower(c.key)
	}
	rowIndex := make(map[string]node)
	cells := make([]string, len(cols))
	for _, item := range n.items {
		indexRowFields(rowIndex, item)
		for i, c := range cols {
			if fv, ok := rowIndex[lowerKeys[i]]; ok {
				cells[i] = formatCellValue(fv, c, true)
			} else {
				cells[i] = "-"
			}
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// renderSingleLine keeps the fields and ordering of the ordinary text
// renderer. A result with its own text form still controls its formatting.
func renderSingleLine(w io.Writer, v any, opts tableOptions) error {
	if handled, err := renderCustomText(w, v, opts); handled {
		return err
	}
	value := toNode(reflect.ValueOf(v))
	if value.kind == nodeObject {
		_, err := fmt.Fprintln(w, strings.Join(singleLineFields(value.fields), "  "))
		return err
	}
	_, err := fmt.Fprintln(w, singleLineValue(value))
	return err
}

func singleLineFields(fields []nodeField) []string {
	items := make([]string, len(fields))
	for i, field := range fields {
		items[i] = singleLineString(field.key) + ": " + singleLineValue(field.value)
	}
	return items
}

func singleLineValue(value node) string {
	switch value.kind {
	case nodeScalar:
		return singleLineString(value.scalar)
	case nodeObject:
		return "{" + strings.Join(singleLineFields(value.fields), ", ") + "}"
	case nodeList:
		items := make([]string, len(value.items))
		for i, item := range value.items {
			items[i] = singleLineValue(item)
		}
		return "[" + strings.Join(items, ", ") + "]"
	default:
		return "null"
	}
}

func singleLineString(value string) string {
	value = SanitizeTerminalText(value)
	if value == "" || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

func writeListOpts(w io.Writer, n node, opts tableOptions) error {
	if isScalarList(n) {
		for _, item := range n.items {
			if _, err := fmt.Fprintln(w, SanitizeTerminalText(item.scalar)); err != nil {
				return err
			}
		}
		return nil
	}
	return writeTableOpts(w, n, opts)
}

func isScalarList(n node) bool {
	if len(n.columns) > 0 {
		return false
	}
	for _, item := range n.items {
		if item.kind != nodeScalar {
			return false
		}
	}
	return true
}

// writeObjectOpts writes one "key: value" line per field. A nested object or a
// list of objects follows its key, indented, inheriting table formatting options.
func writeObjectOpts(w io.Writer, n node, opts tableOptions) error {
	width := 0
	for _, f := range n.fields {
		if inline(f.value) {
			width = max(width, utf8.RuneCountInString(f.key))
		}
	}
	for _, f := range n.fields {
		if inline(f.value) {
			value := SanitizeTerminalText(inlineValue(f.value))
			pad := strings.Repeat(" ", width-utf8.RuneCountInString(f.key))
			lines := strings.Split(value, "\n")
			if _, err := fmt.Fprintf(w, "%s:%s %s\n", SanitizeSingleLine(f.key), pad, lines[0]); err != nil {
				return err
			}
			cont := strings.Repeat(" ", width+2)
			for _, line := range lines[1:] {
				if _, err := fmt.Fprintf(w, "%s%s\n", cont, line); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%s:\n", SanitizeSingleLine(f.key)); err != nil {
			return err
		}
		iw := indentWriter(w, "  ")
		nestedOpts := opts
		if nestedOpts.termWidth > 2 {
			nestedOpts.termWidth -= 2
		}
		var err error
		if f.value.kind == nodeList {
			err = writeTableOpts(iw, f.value, nestedOpts)
		} else {
			err = writeObjectOpts(iw, f.value, nestedOpts)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// inline reports whether a value fits on its key's line: a scalar, a list of
// scalars, or an empty list or object.
func inline(n node) bool {
	switch n.kind {
	case nodeScalar:
		return true
	case nodeList:
		return len(n.items) == 0 || isScalarList(n)
	case nodeObject:
		return len(n.fields) == 0
	case nodeNull:
		return true
	}
	return true
}

func inlineValue(n node) string {
	switch n.kind {
	case nodeScalar:
		return n.scalar
	case nodeList:
		if len(n.items) == 0 {
			return "-"
		}
		parts := make([]string, len(n.items))
		for i, item := range n.items {
			parts[i] = item.scalar
		}
		return strings.Join(parts, ", ")
	case nodeNull, nodeObject:
		// inline admits only an empty object, which prints as a dash too.
		return "-"
	}
	return "-"
}

// sanitizeQuietLine strips terminal escape sequences and line breaks while
// preserving intentional tab separators (such as "<agent_id>\t<handle>").
func sanitizeQuietLine(s string) string {
	return quietLineReplacer.Replace(SanitizeTerminalText(s))
}

// writeIDs prints the id of the result, or the id of each element of the
// list the result holds, one per line. Results implementing QuietRenderer
// control their own lines.
func writeIDs(w io.Writer, v any) error {
	if qr, ok := v.(QuietRenderer); ok {
		for _, line := range qr.QuietLines() {
			if _, err := fmt.Fprintln(w, sanitizeQuietLine(line)); err != nil {
				return err
			}
		}
		return nil
	}
	n := toNode(reflect.ValueOf(v))
	var ids []string
	switch {
	case n.kind == nodeList:
		ids = idsOf(n.items)
	case n.kind == nodeObject && idOf(n) != "":
		ids = []string{idOf(n)}
	default:
		if listField, ok := primaryListField(n); ok {
			ids = idsOf(listField.value.items)
		}
	}
	for _, id := range ids {
		if _, err := fmt.Fprintln(w, sanitizeQuietLine(id)); err != nil {
			return err
		}
	}
	return nil
}

func idsOf(items []node) []string {
	var out []string
	for _, item := range items {
		if id := idOf(item); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func idOf(n node) string {
	for _, f := range n.fields {
		if f.key == "id" && f.value.kind == nodeScalar {
			return f.value.scalar
		}
	}
	return ""
}
