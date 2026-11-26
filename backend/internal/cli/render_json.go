package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
)

// writeEnvelope writes {"<key>": v} as one line of compact JSON.
func writeEnvelope(w io.Writer, key string, v any) error {
	return writeJSONLine(w, map[string]any{key: v})
}

// writeJSONLine marshals v as one compact JSON line ending with '\n'.
func writeJSONLine(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	_, err = w.Write(raw)
	return err
}

// extractPrimaryList locates the top-level slice/array or the primary
// slice/array field of a wrapper struct or map without converting all elements
// to node trees.
func extractPrimaryList(v any) (reflect.Value, bool) {
	if _, isMarshaler := v.(json.Marshaler); isMarshaler {
		return reflect.Value{}, false
	}
	rv, ok := derefValue(reflect.ValueOf(v))
	if !ok {
		return reflect.Value{}, false
	}
	if lv, ok := isListValue(rv); ok {
		return lv, true
	}
	var (
		visibleCount int
		soleList     reflect.Value
		soleIsList   bool
		objListCount int
		objList      reflect.Value
		anyListCount int
		anyList      reflect.Value
	)
	recordField := func(fv reflect.Value) {
		visibleCount++
		lv, ok := isListValue(fv)
		if !ok {
			soleIsList = false
			return
		}
		if visibleCount == 1 {
			soleList = lv
			soleIsList = true
		} else {
			soleIsList = false
		}
		anyList = lv
		anyListCount++
		if isObjectListValue(lv) {
			objList = lv
			objListCount++
		}
	}

	switch rv.Kind() {
	case reflect.Struct:
		desc := getStructDescriptor(rv.Type())
		for _, spec := range desc.fields {
			fv, ok := fieldByIndexSafe(rv, spec.index)
			if !ok || spec.embedMarshaler {
				continue
			}
			if (spec.omitEmpty && isEmptyJSONValue(fv)) || (spec.omitZero && isZeroJSONValue(fv)) {
				continue
			}
			if _, valid := derefValue(fv); !valid {
				continue
			}
			recordField(fv)
		}
	case reflect.Map:
		iter := rv.MapRange()
		for iter.Next() {
			fv := iter.Value()
			if _, valid := derefValue(fv); !valid {
				continue
			}
			recordField(fv)
		}
	default:
		return reflect.Value{}, false
	}

	if visibleCount == 1 && soleIsList {
		return soleList, true
	}
	if objListCount == 1 {
		return objList, true
	}
	if anyListCount == 1 {
		return anyList, true
	}
	return reflect.Value{}, false
}

// writeNDJSON emits one compact JSON object per line. For a list wrapper
// ({"items": [...]}) or slice/array, each item prints on its own line.
func writeNDJSON(w io.Writer, v any, fields []string) error {
	var want map[string]bool
	if len(fields) > 0 {
		want = fieldSet(fields)
	}
	if list, ok := extractPrimaryList(v); ok {
		return writeNDJSONValue(w, list, want)
	}
	if len(want) > 0 {
		v = projectValueFieldsWithSet(v, want)
	}
	return writeJSONLine(w, v)
}

func writeNDJSONSlice(w io.Writer, v any, fields []string) error {
	var want map[string]bool
	if len(fields) > 0 {
		want = fieldSet(fields)
	}
	if list, ok := isListValue(reflect.ValueOf(v)); ok {
		return writeNDJSONValue(w, list, want)
	}
	if len(want) > 0 {
		v = projectValueFieldsWithSet(v, want)
	}
	return writeJSONLine(w, v)
}

func writeNDJSONValue(w io.Writer, rv reflect.Value, want map[string]bool) error {
	for i := range rv.Len() {
		elemVal := rv.Index(i)
		var elem any
		if elemVal.CanInterface() {
			elem = elemVal.Interface()
		} else {
			elem = nodeToJSONValue(toNode(elemVal))
		}
		if len(want) > 0 {
			elem = projectValueFieldsWithSet(elem, want)
		}
		if err := writeJSONLine(w, elem); err != nil {
			return err
		}
	}
	return nil
}

// nodeToJSONValue converts a node to plain JSON-marshalable Go values for the
// rare case where a reflect.Value cannot call Interface() (such as an array
// element inside an unexported embedded struct).
func nodeToJSONValue(n node) any {
	switch n.kind {
	case nodeScalar:
		return n.scalar
	case nodeObject:
		m := make(map[string]any, len(n.fields))
		for _, f := range n.fields {
			m[f.key] = nodeToJSONValue(f.value)
		}
		return m
	case nodeList:
		out := make([]any, len(n.items))
		for i, item := range n.items {
			out[i] = nodeToJSONValue(item)
		}
		return out
	default:
		return nil
	}
}

// projectValueFields filters a JSON-serializable object, slice, or list wrapper
// (holding a primary list of objects alongside metadata) to only the requested
// fields while preserving exact numeric precision.
func projectValueFields(v any, fields []string) any {
	want := fieldSet(fields)
	if len(want) == 0 {
		return v
	}
	return projectValueFieldsWithSet(v, want)
}

func projectValueFieldsWithSet(v any, want map[string]bool) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return v
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		return v
	}
	switch val := decoded.(type) {
	case []any:
		return projectJSONList(val, want)
	case map[string]any:
		return projectJSONObject(val, want)
	default:
		return v
	}
}

func projectJSONList(list []any, want map[string]bool) []any {
	out := make([]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, filterMap(m, want))
		} else {
			out = append(out, item)
		}
	}
	return out
}

func primaryJSONListKey(obj map[string]any) (key string, isNonEmptyObjList bool, ok bool) {
	objKey := ""
	objCount := 0
	emptyKey := ""
	emptyCount := 0
	for k, val := range obj {
		list, isSlice := val.([]any)
		if !isSlice {
			continue
		}
		if len(list) == 0 {
			emptyKey = k
			emptyCount++
			continue
		}
		if _, isMap := list[0].(map[string]any); isMap {
			objKey = k
			objCount++
		}
	}
	if objCount == 1 {
		return objKey, true, true
	}
	if objCount > 1 {
		return "", false, false
	}
	if emptyCount == 1 {
		return emptyKey, false, true
	}
	return "", false, false
}

func jsonListHasAnyField(list []any, want map[string]bool) bool {
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		for k := range m {
			if want[strings.ToLower(k)] {
				return true
			}
		}
	}
	return false
}

func projectJSONObject(obj map[string]any, want map[string]bool) map[string]any {
	topKeys := make(map[string]bool, len(obj))
	for k := range obj {
		topKeys[strings.ToLower(k)] = true
	}
	anyTopMatch := false
	allWantMatchTop := len(want) > 0
	for f := range want {
		if topKeys[f] {
			anyTopMatch = true
		} else {
			allWantMatchTop = false
		}
	}

	if listKey, isNonEmptyObjList, ok := primaryJSONListKey(obj); ok {
		list := obj[listKey].([]any)
		shouldProjectList := (isNonEmptyObjList && (!allWantMatchTop || jsonListHasAnyField(list, want))) ||
			(!isNonEmptyObjList && !anyTopMatch)
		if shouldProjectList {
			outObj := make(map[string]any, len(obj))
			for k, val := range obj {
				if k == listKey {
					outObj[k] = projectJSONList(list, want)
				} else {
					outObj[k] = val
				}
			}
			return outObj
		}
	}
	return filterMap(obj, want)
}

func fieldSet(fields []string) map[string]bool {
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		if token := strings.ToLower(strings.TrimSpace(f)); token != "" {
			want[token] = true
		}
	}
	return want
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

func filterObjectFields(n node, want map[string]bool) node {
	out := node{kind: nodeObject}
	for _, f := range n.fields {
		if want[strings.ToLower(f.key)] {
			out.fields = append(out.fields, f)
		}
	}
	return out
}
