package agent

import (
	"bytes"
	"encoding/json"
)

// jsonEqual compares two encoded values by their compact form, so a difference in
// spacing alone does not read as a different value.
//
// Two providers ask the same question of their own frames: Pi asks whether a second
// resolve pass would produce the value it already has, and Codex asks whether an
// account snapshot moved since the row it wrote. Neither reads a provider's shape, so
// the helper belongs to no provider.
func jsonEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == len(right)
	}
	var leftCompact, rightCompact bytes.Buffer
	if json.Compact(&leftCompact, left) != nil || json.Compact(&rightCompact, right) != nil {
		return false
	}
	return bytes.Equal(leftCompact.Bytes(), rightCompact.Bytes())
}

// JSONCanonicalEqual reports whether two encoded values say the same thing, whatever
// order their object keys arrived in.
//
// Go sorts the keys of a MAP and keeps the declaration order of a STRUCT, so two
// producers of one supplement -- or one producer and the merge that re-encodes it --
// write different bytes for the same content. Every caller that decides "this row
// already carries this supplement" asks THIS rather than comparing bytes: the bytes
// themselves must stay as the agent sent them, because the Raw JSON view shows them.
//
// The byte compare runs first, so the common case costs no parse.
func JSONCanonicalEqual(left, right json.RawMessage) bool {
	if bytes.Equal(left, right) {
		return true
	}
	leftCanonical, err := canonicalJSON(left)
	if err != nil {
		return false
	}
	rightCanonical, err := canonicalJSON(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftCanonical, rightCanonical)
}

// canonicalJSON re-encodes a value with every object's keys in sorted order.
//
// It is a COMPARISON aid for JSONCanonicalEqual and never a storage form: rewriting a
// stored value would reorder the agent's own keys, which the Raw JSON view shows.
//
// Two values it reads as one: an object that REPEATS a key keeps the last one, because
// the decode is into a map. Two spellings of one document then compare equal, and an
// update that only removed the duplicate is dropped. Every producer here is a Go
// marshaler, which never emits one.
//
// Numbers stay as their ORIGINAL bytes. Decoding them into `any` would round every
// integer through a float64 and silently change a value past 2^53, which is a size
// these payloads carry -- Pi and ZCode both send counters that large.
func canonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw, nil
	}
	switch trimmed[0] {
	case '{':
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, err
		}
		for key, value := range fields {
			canonical, err := canonicalJSON(value)
			if err != nil {
				return nil, err
			}
			fields[key] = canonical
		}
		return json.Marshal(fields)
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return nil, err
		}
		for index, value := range items {
			canonical, err := canonicalJSON(value)
			if err != nil {
				return nil, err
			}
			items[index] = canonical
		}
		return json.Marshal(items)
	default:
		// Through `json.Marshal`, so a scalar reads the same at the TOP level as it does
		// inside an object: `Marshal` escapes `<`, `>` and `&` and `Compact` does not,
		// and the two answered differently for one value by depth alone.
		var value json.RawMessage
		if err := json.Unmarshal(trimmed, &value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
}
