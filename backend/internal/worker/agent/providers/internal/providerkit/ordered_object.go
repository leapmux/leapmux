package providerkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// OrderedObject is a JSON object that keeps its members in the order in
// which they were first set. A prompt builds a call's arguments with it, so
// the arguments read in the order of the request's own fields, and the order
// that the reader adds its own fields in.
//
// The zero value is an empty object, ready to use.
type OrderedObject struct {
	keys   []string
	values map[string]json.RawMessage
}

// ReadOrderedObject reads raw as a JSON object, in its own order. A member
// that raw states twice keeps its first place and its last value, as
// JSON.parse reads it. A value that is no JSON object reads as an empty
// object.
func ReadOrderedObject(raw json.RawMessage) *OrderedObject {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return &OrderedObject{}
	}
	object := &OrderedObject{}
	for decoder.More() {
		token, err := decoder.Token()
		key, isKey := token.(string)
		if err != nil || !isKey {
			return &OrderedObject{}
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return &OrderedObject{}
		}
		object.Set(key, value)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return &OrderedObject{}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return &OrderedObject{}
	}
	return object
}

// Set gives key the value. A key that the object holds keeps its place and
// takes the new value, as a key of a JavaScript object spread does.
func (o *OrderedObject) Set(key string, value json.RawMessage) {
	if o.values == nil {
		o.values = make(map[string]json.RawMessage)
	}
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = value
}

// Get returns the value of key, or nil for a key that the object does not
// hold.
func (o *OrderedObject) Get(key string) json.RawMessage {
	return o.values[key]
}

// Merge sets each member of other, in its order, as JavaScript's
// Object.assign does.
func (o *OrderedObject) Merge(other *OrderedObject) {
	if other == nil {
		return
	}
	for _, key := range other.keys {
		o.Set(key, other.values[key])
	}
}

// Encode returns the object as compact JSON text, with the keys written as
// JSONStringify writes them.
func (o *OrderedObject) Encode() json.RawMessage {
	var text bytes.Buffer
	text.WriteByte('{')
	for i, key := range o.keys {
		if i > 0 {
			text.WriteByte(',')
		}
		text.Write(JSONText(key))
		text.WriteByte(':')
		if err := json.Compact(&text, o.values[key]); err != nil {
			// A value is a member that ReadOrderedObject decoded, or a value that
			// a caller set. Only a caller that sets text that is not JSON makes
			// the object invalid, which is a programming error.
			panic(fmt.Sprintf("providerkit: encode an ordered JSON object: %v", err))
		}
	}
	text.WriteByte('}')
	return text.Bytes()
}
