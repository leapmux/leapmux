package cli

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The text form of a result is derived from the same Go value that JSON
// encodes, so the two forms cannot disagree about which fields a result has.
//
// A struct field appears under its `json` name, and `omitempty` hides it in
// text as it does in JSON. A list of structs prints as a table. When the
// element type tags any field with `table:"HEADER"`, the table shows exactly
// the tagged fields, in field order. `table:"HEADER,short"` prints the first
// shortIDLength characters of the value, `table:"HEADER,state"` styles the
// cell through StateClassifier, and `table:"HEADER,dim"` dims the column on
// interactive terminals.

// shortIDLength is the number of characters that a short id prints.
const shortIDLength = 8

// ShortID returns the first shortIDLength characters of id, or id when it is
// shorter: the form that a table prints an id in. A command that identifies an
// object in a line of text uses it too, so one id reads the same in a table
// and in that line. The cut counts characters, not bytes.
func ShortID(id string) string {
	if utf8.RuneCountInString(id) <= shortIDLength {
		return id
	}
	return string([]rune(id)[:shortIDLength])
}

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
	kind       nodeKind
	scalar     string
	fields     []nodeField
	items      []node
	columns    []column
	objectList bool
}

func (n node) isObjectList() bool {
	return n.objectList || len(n.columns) > 0 || (len(n.items) > 0 && n.items[0].kind == nodeObject)
}

type nodeField struct {
	key   string
	value node
}

type column struct {
	key    string
	header string
	short  bool
	state  bool
	dim    bool
}

type isZeroer interface {
	IsZero() bool
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

func derefValue(v reflect.Value) (reflect.Value, bool) {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}, false
		}
		v = v.Elem()
	}
	return v, v.IsValid()
}

func customValueNode(v reflect.Value) (node, bool) {
	if !v.CanInterface() {
		return node{}, false
	}
	if v.Type().Implements(textValuerType) {
		return node{kind: nodeScalar, scalar: v.Interface().(TextValuer).TextValue()}, true
	}
	if v.Type().Implements(marshalerType) {
		// The text form shows what JSON encodes, so a type that encodes
		// itself is read back from its own encoding.
		raw, err := v.Interface().(json.Marshaler).MarshalJSON()
		if err != nil {
			return node{kind: nodeScalar, scalar: fmt.Sprintf("<%v>", err)}, true
		}
		return jsonNode(raw), true
	}
	return node{}, false
}

func toNode(v reflect.Value) node {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return node{}
		}
		if v.Kind() == reflect.Pointer && v.CanInterface() {
			elemType := v.Type().Elem()
			if elemType != timeType && elemType != rawMessageType {
				if n, ok := customValueNode(v); ok {
					return n
				}
			}
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return node{}
	}
	switch {
	case v.Type() == timeType:
		if v.CanInterface() {
			t := v.Interface().(time.Time)
			if t.IsZero() {
				return node{}
			}
			return node{kind: nodeScalar, scalar: t.UTC().Format(time.RFC3339)}
		}
	case v.Type() == rawMessageType:
		return jsonNode(v.Bytes())
	default:
		if n, ok := customValueNode(v); ok {
			return n
		}
		if v.CanAddr() && v.Addr().CanInterface() {
			if n, ok := customValueNode(v.Addr()); ok {
				return n
			}
		}
	}
	switch v.Kind() {
	case reflect.Struct:
		return structNode(v)
	case reflect.Map:
		return mapNode(v)
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.Uint8 {
			if v.IsNil() {
				return node{}
			}
			return node{kind: nodeScalar, scalar: base64.StdEncoding.EncodeToString(v.Bytes())}
		}
		out := node{
			kind:       nodeList,
			columns:    columnsOf(v.Type().Elem()),
			objectList: isObjectListValue(v),
		}
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
		return node{kind: nodeScalar, scalar: strconv.FormatFloat(v.Float(), 'g', -1, v.Type().Bits())}
	default:
		if v.CanInterface() {
			return node{kind: nodeScalar, scalar: fmt.Sprint(v.Interface())}
		}
		return node{kind: nodeScalar, scalar: fmt.Sprint(v)}
	}
}

type structFieldSpec struct {
	index          []int
	name           string
	omitEmpty      bool
	omitZero       bool
	embedMarshaler bool
}

type structDescriptor struct {
	fields  []structFieldSpec
	columns []column
}

var structDescCache sync.Map // map[reflect.Type]*structDescriptor

func getStructDescriptor(t reflect.Type) *structDescriptor {
	if cached, ok := structDescCache.Load(t); ok {
		return cached.(*structDescriptor)
	}
	desc := &structDescriptor{}
	visited := map[reflect.Type]bool{t: true}
	buildStructDescriptor(t, nil, visited, desc)
	actual, _ := structDescCache.LoadOrStore(t, desc)
	return actual.(*structDescriptor)
}

func buildStructDescriptor(t reflect.Type, prefix []int, visited map[reflect.Type]bool, desc *structDescriptor) {
	for i := range t.NumField() {
		sf := t.Field(i)
		isUnexported := !sf.IsExported()
		elemType := indirectType(sf.Type)
		if isUnexported {
			// Mirror encoding/json: ignore unexported fields unless they are
			// anonymous embedded structs whose exported fields get promoted.
			if !sf.Anonymous || elemType.Kind() != reflect.Struct {
				continue
			}
		}
		jt := parseJSONTag(sf)
		if jt.skip {
			continue
		}
		idx := make([]int, len(prefix)+1)
		copy(idx, prefix)
		idx[len(prefix)] = i

		if sf.Anonymous && jt.name == "" && elemType.Kind() == reflect.Struct {
			if elemType != timeType && (sf.Type.Implements(marshalerType) || reflect.PointerTo(elemType).Implements(marshalerType)) {
				desc.fields = append(desc.fields, structFieldSpec{
					index:          idx,
					embedMarshaler: true,
				})
				continue
			}
			if !visited[elemType] {
				visited[elemType] = true
				buildStructDescriptor(elemType, idx, visited, desc)
				delete(visited, elemType)
			}
			continue
		}
		if isUnexported {
			continue
		}
		name := jt.name
		if name == "" {
			name = sf.Name
		}
		desc.fields = append(desc.fields, structFieldSpec{
			index:     idx,
			name:      name,
			omitEmpty: jt.omitEmpty,
			omitZero:  jt.omitZero,
		})
		if tag, ok := sf.Tag.Lookup("table"); ok {
			parts := strings.Split(tag, ",")
			col := column{key: name, header: parts[0]}
			for _, opt := range parts[1:] {
				switch opt {
				case "short":
					col.short = true
				case "state":
					col.state = true
				case "dim":
					col.dim = true
				}
			}
			desc.columns = append(desc.columns, col)
		}
	}
}

func fieldByIndexSafe(v reflect.Value, index []int) (reflect.Value, bool) {
	for _, idx := range index {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(idx)
	}
	return v, true
}

// structNode lists the fields of a struct in declaration order, under their
// JSON names. An embedded struct without a JSON name contributes its fields,
// as encoding/json does.
func structNode(v reflect.Value) node {
	desc := getStructDescriptor(v.Type())
	out := node{kind: nodeObject}
	for _, spec := range desc.fields {
		fv, ok := fieldByIndexSafe(v, spec.index)
		if !ok {
			continue
		}
		if spec.embedMarshaler {
			embedded := toNode(fv)
			out.fields = append(out.fields, embedded.fields...)
			continue
		}
		if (spec.omitEmpty && isEmptyJSONValue(fv)) || (spec.omitZero && isZeroJSONValue(fv)) {
			continue
		}
		child := toNode(fv)
		if child.kind == nodeNull {
			continue
		}
		out.fields = append(out.fields, nodeField{key: spec.name, value: child})
	}
	return out
}

func mapNode(v reflect.Value) node {
	out := node{kind: nodeObject}
	type mapEntry struct {
		key string
		val reflect.Value
	}
	entries := make([]mapEntry, 0, v.Len())
	iter := v.MapRange()
	for iter.Next() {
		k := iter.Key()
		var key string
		if k.Kind() == reflect.String {
			key = k.String()
		} else if k.CanInterface() {
			key = fmt.Sprint(k.Interface())
		} else {
			key = fmt.Sprint(k)
		}
		entries = append(entries, mapEntry{key: key, val: iter.Value()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].key < entries[j].key })
	for _, e := range entries {
		child := toNode(e.val)
		if child.kind == nodeNull {
			continue
		}
		out.fields = append(out.fields, nodeField{key: e.key, value: child})
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
			if len(out.items) > 0 && out.items[0].kind == nodeObject {
				out.objectList = true
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

// isZeroJSONValue reports whether `omitzero` omits v. It mirrors encoding/json
// by checking interface{ IsZero() bool } before falling back to reflect.Value.IsZero.
func isZeroJSONValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			return true
		}
		v = v.Elem()
	}
	if v.Kind() == reflect.Pointer && v.IsNil() {
		return true
	}
	if v.CanInterface() {
		if z, ok := v.Interface().(isZeroer); ok {
			return z.IsZero()
		}
	}
	if v.CanAddr() && v.Addr().CanInterface() {
		if z, ok := v.Addr().Interface().(isZeroer); ok {
			return z.IsZero()
		}
	}
	return v.IsZero()
}

// columnsOf returns the table columns that the element type declares with
// `table` tags, or nil when it declares none.
func columnsOf(elem reflect.Type) []column {
	elem = indirectType(elem)
	if elem.Kind() != reflect.Struct {
		return nil
	}
	return getStructDescriptor(elem).columns
}

// primaryListField finds the sole list-of-objects (or table-tagged) field inside
// a wrapper struct such as paginated lists, notification lists, queue lists, or
// search results, so --format tsv, --format ndjson, and --fields operate on the
// rows even when sibling metadata fields are present. Scalar slices alongside
// sibling fields are never unwrapped; single-field wrappers still unwrap.
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
		if f.value.isObjectList() {
			candidate = f
			count++
		}
	}
	if count == 1 {
		return candidate, true
	}
	return nodeField{}, false
}

// isListValue reports whether rv (after dereferencing pointers/interfaces) is a
// non-byte slice or an array.
func isListValue(rv reflect.Value) (reflect.Value, bool) {
	rv, ok := derefValue(rv)
	if !ok {
		return reflect.Value{}, false
	}
	if rv.Kind() == reflect.Slice && rv.Type().Elem().Kind() == reflect.Uint8 {
		return reflect.Value{}, false
	}
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return rv, true
	}
	return reflect.Value{}, false
}

// isObjectListValue reports whether a slice/array value holds objects (structs
// or maps, including empty slices of structs or maps), matching
// primaryListField's rule without building a node tree for every element.
func isObjectListValue(lv reflect.Value) bool {
	elemType := indirectType(lv.Type().Elem())
	switch elemType.Kind() {
	case reflect.Struct:
		if elemType == timeType {
			return false
		}
		if len(columnsOf(elemType)) > 0 {
			return true
		}
		if elemType.Implements(textValuerType) || reflect.PointerTo(elemType).Implements(textValuerType) {
			return false
		}
		return true
	case reflect.Map:
		return true
	case reflect.Interface:
		if lv.Len() == 0 {
			return false
		}
		first, ok := derefValue(lv.Index(0))
		if !ok {
			return false
		}
		if first.Kind() == reflect.Map {
			return true
		}
		if first.Kind() == reflect.Struct && first.Type() != timeType {
			return !first.Type().Implements(textValuerType) && !reflect.PointerTo(first.Type()).Implements(textValuerType)
		}
		return false
	default:
		return false
	}
}
