// Package optionmap defines the agent option id->value map and the two wire conventions
// every option-map handler must respect. It is a LEAF package (importing only the standard
// library), so both the worker/agent runtime (which applies option deltas to a running agent)
// and the worker/service layer (which persists them to the agents.options column) share ONE
// type and ONE statement of the contract -- rather than re-asserting "empty value deletes /
// never store empty" in prose at each boundary.
//
// The same shape appears as the agents.options column, the proto Settings.Options map, the
// launch-option set, and a refresh DELTA. The empty-value semantics unify those uses:
//   - a STORE map drops empty values (Marshal) -- an empty value is never persisted;
//   - a DELTA map carries an empty value as an explicit DELETE (Merge) -- so a cleared axis
//     removes its stored value instead of leaving a stale one, while an OMITTED key means
//     "no change" and preserves whatever is stored.
package optionmap

import (
	"encoding/json"
	"log/slog"
	"maps"
)

// Map is an agent's option id->value map. It is a defined type over map[string]string, so it
// flows to/from the plain-map proto and agent boundaries by Go's assignability rule without
// explicit conversion, while still carrying the conventions below as methods so a caller can't
// accidentally bypass them.
type Map map[string]string

// Get returns the value for id, or "" when absent. nil-safe.
func (m Map) Get(id string) string { return m[id] }

// Clone returns an independent copy (never nil), so the caller can mutate it without touching a
// launch/DB-loaded map that may still be in use elsewhere.
func (m Map) Clone() Map {
	out := maps.Clone(m)
	if out == nil {
		out = Map{}
	}
	return out
}

// Merge overlays incoming onto a clone of m, where an empty incoming value DELETES the key
// (the delta contract: a key OMITTED from incoming means "no change" and preserves the stored
// value, while a key present with an empty value clears it). m is left untouched.
func (m Map) Merge(incoming Map) Map {
	merged := m.Clone()
	for k, v := range incoming {
		if v == "" {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	return merged
}

// Marshal encodes m for the agents.options column, dropping empty values (the store contract).
// json.Marshal sorts map keys, so the output is stable (the options CAS string-compares it).
func (m Map) Marshal() string {
	if len(m) == 0 {
		return "{}"
	}
	filtered := make(map[string]string, len(m))
	for k, v := range m {
		if v != "" {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return "{}"
	}
	data, err := json.Marshal(filtered)
	if err != nil {
		slog.Error("failed to marshal options; using empty object", "error", err)
		return "{}"
	}
	return string(data)
}

// Parse decodes the agents.options JSON column into a Map, dropping empty values. Never nil.
func Parse(raw string) Map {
	if raw == "" {
		return Map{}
	}
	var parsed Map
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		slog.Warn("invalid agent options payload; using empty object", "error", err)
		return Map{}
	}
	if parsed == nil {
		return Map{}
	}
	for k, v := range parsed {
		if v == "" {
			delete(parsed, k)
		}
	}
	return parsed
}
