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
		tag, ok := sf.Tag.Lookup("table")
		if !ok || !sf.IsExported() {
			continue
		}
		jt := parseJSONTag(sf)
		if jt.skip {
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

// renderText writes the text form of v.
func renderText(w io.Writer, v any) error {
	if r, ok := v.(TextRenderer); ok {
		return r.RenderText(w)
	}
	n := toNode(reflect.ValueOf(v))
	switch n.kind {
	case nodeNull:
		return nil
	case nodeScalar:
		_, err := fmt.Fprintln(w, n.scalar)
		return err
	case nodeList:
		return writeList(w, n)
	case nodeObject:
		// An object that holds one list and nothing else is that list: the
		// table needs no heading.
		if len(n.fields) == 1 && n.fields[0].value.kind == nodeList {
			return writeList(w, n.fields[0].value)
		}
		return writeObject(w, n)
	}
	return fmt.Errorf("cli: no text form for node kind %d", n.kind)
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
	if value == "" || strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}) >= 0 {
		return strconv.Quote(value)
	}
	return value
}

func writeList(w io.Writer, n node) error {
	if isScalarList(n) {
		for _, item := range n.items {
			if _, err := fmt.Fprintln(w, item.scalar); err != nil {
				return err
			}
		}
		return nil
	}
	return writeTable(w, n)
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

// writeObject writes one "key: value" line per field. A nested object or a
// list of objects follows its key, indented.
func writeObject(w io.Writer, n node) error {
	width := 0
	for _, f := range n.fields {
		if inline(f.value) {
			width = max(width, utf8.RuneCountInString(f.key))
		}
	}
	for _, f := range n.fields {
		if inline(f.value) {
			value := inlineValue(f.value)
			pad := strings.Repeat(" ", width-utf8.RuneCountInString(f.key))
			lines := strings.Split(value, "\n")
			if _, err := fmt.Fprintf(w, "%s:%s %s\n", f.key, pad, lines[0]); err != nil {
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
		if _, err := fmt.Fprintf(w, "%s:\n", f.key); err != nil {
			return err
		}
		iw := indentWriter(w, "  ")
		var err error
		if f.value.kind == nodeList {
			err = writeTable(iw, f.value)
		} else {
			err = writeObject(iw, f.value)
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

// writeTable writes a list of objects as an aligned table with a header row.
func writeTable(w io.Writer, n node) error {
	cols := n.columns
	if len(cols) == 0 {
		cols = derivedColumns(n)
	}
	if len(cols) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.header
	}
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, item := range n.items {
		cells := make([]string, len(cols))
		for i, c := range cols {
			cells[i] = cellValue(item, c)
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
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

func cellValue(row node, c column) string {
	for _, f := range row.fields {
		if f.key != c.key {
			continue
		}
		value := sanitizeCell(inlineValue(f.value))
		if c.short {
			value = ShortID(value)
		}
		return truncateCell(value)
	}
	return "-"
}

func sanitizeCell(s string) string {
	if s == "" {
		return "-"
	}
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}

func truncateCell(s string) string {
	if utf8.RuneCountInString(s) <= maxCellRunes {
		return s
	}
	return string([]rune(s)[:maxCellRunes-1]) + "…"
}

// writeIDs prints the id of the result, or the id of each element of the
// list the result holds, one per line. A result without an id prints
// nothing.
func writeIDs(w io.Writer, v any) error {
	n := toNode(reflect.ValueOf(v))
	var ids []string
	switch {
	case n.kind == nodeList:
		ids = idsOf(n.items)
	case n.kind == nodeObject && idOf(n) != "":
		ids = []string{idOf(n)}
	case n.kind == nodeObject && len(n.fields) == 1 && n.fields[0].value.kind == nodeList:
		ids = idsOf(n.fields[0].value.items)
	}
	for _, id := range ids {
		if _, err := fmt.Fprintln(w, id); err != nil {
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
