package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"
)

// The text form of a result is derived from the same Go value that JSON
// encodes, so the two forms cannot disagree about which fields a result has.
//
// A struct field appears under its `json` name, and `omitempty` hides it in
// text as it does in JSON. A list of structs prints as a table. When the
// element type tags any field with `table:"HEADER"`, the table shows exactly
// the tagged fields, in field order. `table:"HEADER,short"` prints the first
// shortIDLength characters of the value, for an id: a table is where width
// matters, and a "key: value" view prints the whole id, as `docker ps` and
// `docker inspect` do.

// shortIDLength is the number of characters that a short id prints.
const shortIDLength = 8

// ShortID returns the first shortIDLength characters of id, or id when it is
// shorter: the form that a table prints an id in. A command that names an
// object in a line of text uses it too, so one id reads the same in a table
// and in that line. The cut counts characters, not bytes.
func ShortID(id string) string {
	if utf8.RuneCountInString(id) <= shortIDLength {
		return id
	}
	return string([]rune(id)[:shortIDLength])
}

// maxCellRunes caps one table cell, so that a long title cannot push the
// other columns off the screen. The "key: value" view prints the whole value.
const maxCellRunes = 60

type nodeKind int

const (
	nodeNull nodeKind = iota
	nodeScalar
	nodeObject
	nodeList
)

// node is a result, converted to an ordered tree that keeps the column
// declarations of each list.
type node struct {
	kind    nodeKind
	scalar  string
	fields  []nodeField
	items   []node
	columns []column
}

type nodeField struct {
	key   string
	value node
}

type column struct {
	key    string
	header string
	short  bool
}

var (
	timeType       = reflect.TypeFor[time.Time]()
	rawMessageType = reflect.TypeFor[json.RawMessage]()
	marshalerType  = reflect.TypeFor[json.Marshaler]()
	textValuerType = reflect.TypeFor[TextValuer]()
)

func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}

// sliceOf converts a typed slice to a node for writeTable.
func sliceOf[T any](items []T) node {
	return toNode(reflect.ValueOf(items))
}

func toNode(v reflect.Value) node {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return node{}
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return node{}
	}
	switch {
	case v.Type() == timeType:
		t := v.Interface().(time.Time)
		if t.IsZero() {
			return node{}
		}
		return node{kind: nodeScalar, scalar: t.UTC().Format(time.RFC3339)}
	case v.Type().Implements(textValuerType):
		return node{kind: nodeScalar, scalar: v.Interface().(TextValuer).TextValue()}
	case v.Type() == rawMessageType:
		return jsonNode(v.Bytes())
	case v.Type().Implements(marshalerType):
		// The text form shows what JSON encodes, so a type that encodes
		// itself is read back from its own encoding.
		raw, err := v.Interface().(json.Marshaler).MarshalJSON()
		if err != nil {
			return node{kind: nodeScalar, scalar: fmt.Sprintf("<%v>", err)}
		}
		return jsonNode(raw)
	}
	switch v.Kind() {
	case reflect.Struct:
		return structNode(v)
	case reflect.Map:
		return mapNode(v)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			raw, _ := json.Marshal(v.Interface())
			return jsonNode(raw)
		}
		out := node{kind: nodeList, columns: columnsOf(v.Type().Elem())}
		for i := range v.Len() {
			out.items = append(out.items, toNode(v.Index(i)))
		}
		return out
	case reflect.String:
		return node{kind: nodeScalar, scalar: v.String()}
	case reflect.Bool:
		return node{kind: nodeScalar, scalar: strconv.FormatBool(v.Bool())}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return node{kind: nodeScalar, scalar: strconv.FormatInt(v.Int(), 10)}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return node{kind: nodeScalar, scalar: strconv.FormatUint(v.Uint(), 10)}
	case reflect.Float32, reflect.Float64:
		return node{kind: nodeScalar, scalar: strconv.FormatFloat(v.Float(), 'g', -1, 64)}
	default:
		return node{kind: nodeScalar, scalar: fmt.Sprint(v.Interface())}
	}
}

// structNode lists the fields of a struct in declaration order, under their
// JSON names. An embedded struct without a JSON name contributes its fields,
// as encoding/json does.
func structNode(v reflect.Value) node {
	out := node{kind: nodeObject}
	t := v.Type()
	for i := range t.NumField() {
		sf := t.Field(i)
		if !sf.IsExported() {
			continue
		}
		tag := parseJSONTag(sf)
		if tag.skip {
			continue
		}
		fv := v.Field(i)
		name := tag.name
		if sf.Anonymous && name == "" && indirectType(sf.Type).Kind() == reflect.Struct {
			embedded := toNode(fv)
			out.fields = append(out.fields, embedded.fields...)
			continue
		}
		if name == "" {
			name = sf.Name
		}
		if (tag.omitEmpty && isEmptyJSONValue(fv)) || (tag.omitZero && fv.IsZero()) {
			continue
		}
		child := toNode(fv)
		if child.kind == nodeNull {
			continue
		}
		out.fields = append(out.fields, nodeField{key: name, value: child})
	}
	return out
}

func mapNode(v reflect.Value) node {
	out := node{kind: nodeObject}
	keys := v.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i].Interface()) < fmt.Sprint(keys[j].Interface()) })
	for _, k := range keys {
		child := toNode(v.MapIndex(k))
		if child.kind == nodeNull {
			continue
		}
		out.fields = append(out.fields, nodeField{key: fmt.Sprint(k.Interface()), value: child})
	}
	return out
}

// jsonNode converts encoded JSON to a node, keeping the key order of each
// object.
func jsonNode(raw []byte) node {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	n, err := decodeJSONNode(dec)
	if err != nil {
		return node{kind: nodeScalar, scalar: string(raw)}
	}
	return n
}

func decodeJSONNode(dec *json.Decoder) (node, error) {
	tok, err := dec.Token()
	if err != nil {
		return node{}, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			out := node{kind: nodeObject}
			for dec.More() {
				keyTok, err := dec.Token()
				if err != nil {
					return node{}, err
				}
				child, err := decodeJSONNode(dec)
				if err != nil {
					return node{}, err
				}
				if child.kind != nodeNull {
					out.fields = append(out.fields, nodeField{key: fmt.Sprint(keyTok), value: child})
				}
			}
			_, err := dec.Token()
			return out, err
		case '[':
			out := node{kind: nodeList}
			for dec.More() {
				child, err := decodeJSONNode(dec)
				if err != nil {
					return node{}, err
				}
				out.items = append(out.items, child)
			}
			_, err := dec.Token()
			return out, err
		}
	case nil:
		return node{}, nil
	case string:
		return node{kind: nodeScalar, scalar: t}, nil
	}
	return node{kind: nodeScalar, scalar: fmt.Sprint(tok)}, nil
}

func indirectType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// jsonTag is what a field's `json` tag tells encoding/json.
type jsonTag struct {
	name      string
	omitEmpty bool
	omitZero  bool
	skip      bool
}

func parseJSONTag(sf reflect.StructField) jsonTag {
	tag, ok := sf.Tag.Lookup("json")
	if !ok {
		return jsonTag{}
	}
	if tag == "-" {
		return jsonTag{skip: true}
	}
	parts := strings.Split(tag, ",")
	out := jsonTag{name: parts[0]}
	for _, opt := range parts[1:] {
		switch opt {
		case "omitempty":
			out.omitEmpty = true
		case "omitzero":
			out.omitZero = true
		}
	}
	return out
}

// isEmptyJSONValue reports whether `omitempty` omits v. It mirrors
// encoding/json: a struct is never empty, and an empty but non-nil slice is.
func isEmptyJSONValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
		return v.Len() == 0
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Interface, reflect.Pointer:
		return v.IsZero()
	default:
		return false
	}
}

// columnsOf returns the table columns that the element type declares with
// `table` tags, or nil when it declares none.
func columnsOf(elem reflect.Type) []column {
	elem = indirectType(elem)
	if elem.Kind() != reflect.Struct {
		return nil
	}
	var out []column
	for i := range elem.NumField() {
		sf := elem.Field(i)
		if !sf.IsExported() {
			continue
		}
		jt := parseJSONTag(sf)
		if jt.skip {
			continue
		}
		if sf.Anonymous && jt.name == "" && indirectType(sf.Type).Kind() == reflect.Struct {
			out = append(out, columnsOf(sf.Type)...)
			continue
		}
		tag, ok := sf.Tag.Lookup("table")
		if !ok {
			continue
		}
		name := jt.name
		if name == "" {
			name = sf.Name
		}
		parts := strings.Split(tag, ",")
		col := column{key: name, header: parts[0]}
		for _, opt := range parts[1:] {
			if opt == "short" {
				col.short = true
			}
		}
		out = append(out, col)
	}
	return out
}

// tableOptions controls table/TSV rendering (width adaptation, headers, field
// selection, and TTY-only ANSI styling).
type tableOptions struct {
	wide      bool
	noHeader  bool
	fields    []string
	termWidth int
	style     styler
}

// TableOptions holds rendering options passed to an OptionsTextRenderer so
// custom table views honor --wide, --no-header, --color, and terminal width.
type TableOptions struct {
	opts tableOptions
}

// OptionsTextRenderer is an optional alternative to TextRenderer for values
// that embed aligned tables and want to honor --wide, --no-header, --color,
// and TTY column width.
type OptionsTextRenderer interface {
	RenderTextWithOptions(w io.Writer, opts TableOptions) error
}

// WriteTable writes v (a slice of structs) as an aligned table using the
// standard column and sanitization rules.
func WriteTable(w io.Writer, v any) error {
	return writeTableOpts(w, toNode(reflect.ValueOf(v)), tableOptions{})
}

// WriteTableWithOptions writes v as an aligned table honoring opts.
func WriteTableWithOptions(w io.Writer, v any, opts TableOptions) error {
	return writeTableOpts(w, toNode(reflect.ValueOf(v)), opts.opts)
}

// primaryListField finds the sole list-of-objects (or table-tagged) field inside
// a wrapper struct such as paginated admin lists, notification list, queue list,
// or search results, so --format tsv, --format ndjson, and --fields operate on
// the rows even when sibling metadata fields (like next_cursor, truncated,
// paused, or worker_id) are present.
func primaryListField(n node) (nodeField, bool) {
	if n.kind != nodeObject {
		return nodeField{}, false
	}
	if len(n.fields) == 1 && n.fields[0].value.kind == nodeList {
		return n.fields[0], true
	}
	var candidate nodeField
	count := 0
	for _, f := range n.fields {
		if f.value.kind != nodeList {
			continue
		}
		// Prefer lists of objects (or lists with table columns declared on the element type).
		if len(f.value.columns) > 0 || (len(f.value.items) > 0 && f.value.items[0].kind == nodeObject) {
			candidate = f
			count++
		}
	}
	if count == 1 {
		return candidate, true
	}
	// Fallback: if there is exactly one list field of any kind alongside scalars
	// (e.g., an empty slice of structs whose element type had no table tags).
	count = 0
	for _, f := range n.fields {
		if f.value.kind == nodeList {
			candidate = f
			count++
		}
	}
	if count == 1 {
		return candidate, true
	}
	return nodeField{}, false
}

// renderTextOpts writes the text form of v honoring tableOptions.
func renderTextOpts(w io.Writer, v any, opts tableOptions) error {
	if r, ok := v.(OptionsTextRenderer); ok && len(opts.fields) == 0 {
		return r.RenderTextWithOptions(w, TableOptions{opts: opts})
	}
	if r, ok := v.(TextRenderer); ok && len(opts.fields) == 0 {
		return r.RenderText(w)
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
			if listField, ok := primaryListField(n); ok && !objectHasAnyField(n, opts.fields) {
				return writeListOpts(w, listField.value, opts)
			}
			n = filterObjectFields(n, opts.fields)
		}
		return writeObjectOpts(w, n, opts)
	}
	return fmt.Errorf("cli: no text form for node kind %d", n.kind)
}

func objectHasAnyField(n node, fields []string) bool {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[strings.ToLower(strings.TrimSpace(f))] = true
	}
	for _, f := range n.fields {
		if want[strings.ToLower(f.key)] {
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
			n = filterObjectFields(n, opts.fields)
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
		n = filterObjectFields(n, opts.fields)
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
	for _, item := range n.items {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = rawCellValue(item, c, true)
		}
		if _, err := fmt.Fprintln(w, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return nil
}

// writeNDJSON emits one compact JSON object per line. For a list wrapper
// ({"items": [...]}) or slice, each item prints on its own line.
func writeNDJSON(w io.Writer, v any, fields []string) error {
	n := toNode(reflect.ValueOf(v))
	if listField, ok := primaryListField(n); ok {
		return writeNDJSONSlice(w, extractNamedSlice(v, listField.key), fields)
	}
	if n.kind == nodeList {
		return writeNDJSONSlice(w, v, fields)
	}
	if len(fields) > 0 {
		v = projectValueFields(v, fields)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

func extractNamedSlice(v any, jsonKey string) any {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		rv = rv.Elem()
	}
	if rv.IsValid() && rv.Kind() == reflect.Struct {
		rt := rv.Type()
		for i := range rv.NumField() {
			sf := rt.Field(i)
			if !sf.IsExported() || rv.Field(i).Kind() != reflect.Slice {
				continue
			}
			if tag := parseJSONTag(sf); !tag.skip && tag.name == jsonKey {
				return rv.Field(i).Interface()
			}
		}
		for i := range rv.NumField() {
			if rv.Type().Field(i).IsExported() && rv.Field(i).Kind() == reflect.Slice {
				return rv.Field(i).Interface()
			}
		}
	}
	return v
}

func writeNDJSONSlice(w io.Writer, v any, fields []string) error {
	rv := reflect.ValueOf(v)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		rv = rv.Elem()
	}
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		_, err = w.Write(raw)
		return err
	}
	for i := range rv.Len() {
		elem := rv.Index(i).Interface()
		if len(fields) > 0 {
			elem = projectValueFields(elem, fields)
		}
		raw, err := json.Marshal(elem)
		if err != nil {
			return err
		}
		raw = append(raw, '\n')
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
	return nil
}

// projectValueFields filters a JSON-serializable object (or list wrapper
// holding a primary list of objects alongside metadata) to only the requested
// fields.
func projectValueFields(v any, fields []string) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return v
	}
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[strings.ToLower(strings.TrimSpace(f))] = true
	}
	// If none of the top-level keys match the requested fields and the object
	// wraps a primary list of objects (e.g., {"items":[...],"next_cursor":""}),
	// project each element of that list while preserving sibling metadata.
	anyTopMatch := false
	for k := range obj {
		if want[strings.ToLower(k)] {
			anyTopMatch = true
			break
		}
	}
	if !anyTopMatch {
		listKey := ""
		listCount := 0
		for k, val := range obj {
			if _, ok := val.([]any); ok {
				listKey = k
				listCount++
			}
		}
		if listCount == 1 {
			list := obj[listKey].([]any)
			outList := make([]any, 0, len(list))
			for _, item := range list {
				if m, ok := item.(map[string]any); ok {
					outList = append(outList, filterMap(m, want))
				} else {
					outList = append(outList, item)
				}
			}
			outObj := make(map[string]any, len(obj))
			for k, val := range obj {
				if k == listKey {
					outObj[k] = outList
				} else {
					outObj[k] = val
				}
			}
			return outObj
		}
	}
	return filterMap(obj, want)
}

func filterMap(m map[string]any, want map[string]bool) map[string]any {
	out := make(map[string]any, len(want))
	for k, v := range m {
		if want[strings.ToLower(k)] {
			out[k] = v
		}
	}
	return out
}

func filterObjectFields(n node, fields []string) node {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[strings.ToLower(strings.TrimSpace(f))] = true
	}
	out := node{kind: nodeObject}
	for _, f := range n.fields {
		if want[strings.ToLower(f.key)] {
			out.fields = append(out.fields, f)
		}
	}
	return out
}

// renderSingleLine keeps the fields and ordering of the ordinary text
// renderer. A result with its own text form still controls its formatting.
func renderSingleLine(w io.Writer, v any) error {
	if r, ok := v.(TextRenderer); ok {
		return r.RenderText(w)
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

// writeTableOpts writes a list of objects as an aligned table with optional
// header, field selection, terminal-width adaptation, and TTY-only styling.
func writeTableOpts(w io.Writer, n node, opts tableOptions) error {
	cols := selectColumns(n, opts.fields)
	if len(cols) == 0 {
		return nil
	}
	rawRows := make([][]string, len(n.items))
	colWidths := make([]int, len(cols))
	for i, c := range cols {
		colWidths[i] = utf8.RuneCountInString(c.header)
	}
	// When terminal width is known (interactive TTY), do not pre-cap cells at
	// maxCellRunes=60; let fitColumnsToWidth dynamically shrink wide columns to
	// match the actual terminal width so wide monitors show full titles/paths.
	uncapped := opts.wide || opts.termWidth > 0
	for rIdx, item := range n.items {
		row := make([]string, len(cols))
		for cIdx, c := range cols {
			val := rawCellValue(item, c, uncapped)
			row[cIdx] = val
			colWidths[cIdx] = max(colWidths[cIdx], utf8.RuneCountInString(val))
		}
		rawRows[rIdx] = row
	}
	// On an interactive TTY (when --wide is not set), shrink the widest prose
	// column so the table fits comfortably inside the terminal width.
	if !opts.wide && opts.termWidth > 0 && len(cols) > 1 {
		colWidths = fitColumnsToWidth(cols, colWidths, opts.termWidth)
	}
	// Use manual padding when ANSI styling is enabled so invisible escape codes
	// never throw off tabwriter's byte-width calculations.
	if opts.style.enabled {
		if !opts.noHeader {
			var sb strings.Builder
			for i, c := range cols {
				if i > 0 {
					sb.WriteString("  ")
				}
				pad := max(0, colWidths[i]-utf8.RuneCountInString(c.header))
				sb.WriteString(opts.style.dim(c.header))
				if i < len(cols)-1 {
					sb.WriteString(strings.Repeat(" ", pad))
				}
			}
			if _, err := fmt.Fprintln(w, sb.String()); err != nil {
				return err
			}
		}
		for _, row := range rawRows {
			var sb strings.Builder
			for i, val := range row {
				if i > 0 {
					sb.WriteString("  ")
				}
				val = truncateTo(val, colWidths[i])
				pad := max(0, colWidths[i]-utf8.RuneCountInString(val))
				styled := val
				switch {
				case isStateColumn(cols[i]):
					styled = opts.style.stateBadge(val)
				case isDimColumn(cols[i], i):
					styled = opts.style.dim(val)
				}
				sb.WriteString(styled)
				if i < len(cols)-1 {
					sb.WriteString(strings.Repeat(" ", pad))
				}
			}
			if _, err := fmt.Fprintln(w, sb.String()); err != nil {
				return err
			}
		}
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if !opts.noHeader {
		headers := make([]string, len(cols))
		for i, c := range cols {
			headers[i] = c.header
		}
		if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range rawRows {
		cells := make([]string, len(cols))
		for i, val := range row {
			cells[i] = truncateTo(val, colWidths[i])
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func isStateColumn(c column) bool {
	switch strings.ToUpper(c.header) {
	case "STATE", "STATUS", "ONLINE", "HEALTH":
		return true
	}
	return false
}

// isDimColumn identifies secondary ID and timestamp columns dimmed on TTYs.
func isDimColumn(c column, colIndex int) bool {
	if c.short && colIndex > 0 {
		return true
	}
	switch strings.ToUpper(c.header) {
	case "CREATED", "UPDATED", "DATE", "EXPIRES", "LAST_SEEN", "LAST SEEN", "SCHEDULED", "TIME", "AT":
		return true
	}
	return false
}

// fitColumnsToWidth shrinks only wide prose columns when total rendered width
// exceeds termWidth, keeping compact ID/state columns intact.
func fitColumnsToWidth(cols []column, widths []int, termWidth int) []int {
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
		widest := 12
		for i, w := range out {
			if w > widest && !cols[i].short {
				widest = w
				widestIdx = i
			}
		}
		if widestIdx < 0 {
			break
		}
		shrink := min(excess, out[widestIdx]-12)
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

func rawCellValue(row node, c column, wide bool) string {
	for _, f := range row.fields {
		if f.key != c.key {
			continue
		}
		value := sanitizeCell(inlineValue(f.value))
		if c.short {
			value = ShortID(value)
		}
		if wide {
			return value
		}
		return truncateCell(value)
	}
	return "-"
}

func sanitizeCell(s string) string {
	s = SanitizeSingleLine(s)
	if s == "" {
		return "-"
	}
	return s
}

func truncateCell(s string) string {
	if utf8.RuneCountInString(s) <= maxCellRunes {
		return s
	}
	return string([]rune(s)[:maxCellRunes-1]) + "…"
}

// writeIDs prints the id of the result, or the id of each element of the
// list the result holds, one per line. Results implementing QuietRenderer
// control their own lines.
// sanitizeQuietLine strips terminal escape sequences and line breaks while
// preserving intentional tab separators (such as "<agent_id>\t<handle>").
func sanitizeQuietLine(s string) string {
	s = SanitizeTerminalText(s)
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

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
