package usersettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// Registry is the lookup-only face of the account-scope keys. It is
// deliberately NOT a settings.Manager: the manager's process-wide
// snapshot, TTL cache, Subscribe, and epoch-ordered publish are
// instance-scope machinery, none of which fits per-user rows. User scope
// gets a per-request decode-merge-validate over the user's own blob, so
// the only shared state is the key declarations themselves.
type Registry struct {
	byName map[string]settings.Descriptor
	names  []string

	mu     sync.Mutex
	warned map[string]bool
}

// Default is the process-wide registry over the declared account keys.
var Default = newRegistry()

func newRegistry() *Registry {
	r := &Registry{
		byName: make(map[string]settings.Descriptor),
		warned: make(map[string]bool),
	}
	for _, d := range descriptors() {
		registerDescriptor(r, d)
	}
	return r
}

// registerDescriptor adds one declared key, refusing the two shapes the
// account scope cannot carry.
func registerDescriptor(r *Registry, d settings.Descriptor) {
	if _, dup := r.byName[d.Name()]; dup {
		panic(fmt.Sprintf("usersettings: duplicate key %q", d.Name()))
	}
	if d.HasSecret() {
		// The account scope has NO encrypted half. It stores one JSON blob
		// in users.prefs, so a key that declared secret fields would have
		// its secret written in the clear AND rendered by a frontend that
		// trusts the descriptor's secret flag. Refusing at registration is
		// what makes that mistake impossible; adding a redaction step would
		// hide the value on the wire while still storing it in plaintext.
		panic(fmt.Sprintf("usersettings: key %q declares secret fields, which the account scope cannot store encrypted", d.Name()))
	}
	r.byName[d.Name()] = d
	r.names = append(r.names, d.Name())
}

// Descriptors lists the registered descriptors in declaration order.
func (r *Registry) Descriptors() []settings.Descriptor {
	out := make([]settings.Descriptor, 0, len(r.names))
	for _, name := range r.names {
		out = append(out, r.byName[name])
	}
	return out
}

// The account scope shares settings.InvalidError rather than mirroring
// it. Both scopes reject a value the same way and both reach the same RPC
// boundary, so one type means one errors.As there — two hand-mirrored
// twins meant a handler that learned about only one downgraded the other
// to a 500.
var invalidf = settings.Invalidf

// ErrMalformedBlob marks a stored prefs blob that is not a JSON object.
// It is STORED corruption, not caller input, so the RPC surface answers
// FailedPrecondition rather than blaming the request. Typed rather than
// matched on message text: a reworded message must not silently
// reclassify the failure as a 500.
var ErrMalformedBlob = errors.New("prefs blob is not a JSON object")

// decodeBlob parses the prefs blob into raw per-key documents. An empty
// or non-object blob decodes to an empty document (every key at its
// default); a malformed one is an error the caller refuses on.
//
// This is the ONLY place the blob layout is known. A caller that
// unmarshals the blob itself keeps a second copy of that format, which is
// how the read path ended up parsing the whole document once per key.
func decodeBlob(prefsJSON string) (map[string]json.RawMessage, error) {
	doc := make(map[string]json.RawMessage)
	if prefsJSON == "" {
		return doc, nil
	}
	if err := json.Unmarshal([]byte(prefsJSON), &doc); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformedBlob, err)
	}
	return doc, nil
}

// State is one key's resolved value plus whether the blob stores one.
type State struct {
	Value      any
	Raw        json.RawMessage
	Customized bool
}

// States resolves EVERY registered key against the blob in one pass:
// the decoded value, its stored sub-document, and whether it is stored.
// The listing path wants all three per key, and doing it in one pass
// keeps the blob parsed once instead of once per key.
func (r *Registry) States(prefsJSON string) map[string]State {
	doc := r.parseBlob(prefsJSON)
	out := make(map[string]State, len(r.names))
	for _, name := range r.names {
		out[name] = r.stateFrom(doc, name)
	}
	return out
}

// State resolves ONE key against the blob.
//
// The write path answers with the key it just wrote, and nothing else, so
// it decodes and validates that key alone. Going through States there
// resolved all nine account keys and discarded eight — work that grows
// with every key added, on every toggle flip, slider release and font
// edit.
func (r *Registry) State(prefsJSON, key string) (State, bool) {
	if _, ok := r.byName[key]; !ok {
		return State{}, false
	}
	return r.stateFrom(r.parseBlob(prefsJSON), key), true
}

// parseBlob is the READ path's blob parse: an undecodable blob degrades
// to "no stored value for any key" with a one-time warning, mirroring
// settings.Manager.buildSnapshotWith rather than failing the read. The write
// path calls decodeBlob directly instead, because it must refuse rather
// than overwrite a document it cannot read.
func (r *Registry) parseBlob(prefsJSON string) map[string]json.RawMessage {
	doc, err := decodeBlob(prefsJSON)
	if err != nil {
		r.warnOnce("blob", "user prefs blob undecodable; using defaults for every key", "error", err)
		return map[string]json.RawMessage{}
	}
	return doc
}

// stateFrom resolves one key against an already-parsed blob. Both State
// and States go through it, so a single-key read and a whole-blob read
// can never resolve the same key differently.
func (r *Registry) stateFrom(doc map[string]json.RawMessage, name string) State {
	raw, present := doc[name]
	return State{Value: r.decodeOne(name, raw, present), Raw: raw, Customized: present}
}

// decodeOne resolves one key's stored sub-document, degrading to the key's
// default (with a one-time warning) when it is absent, undecodable, or
// invalid.
func (r *Registry) decodeOne(name string, raw json.RawMessage, present bool) any {
	desc := r.byName[name]
	if !present {
		return desc.Default()
	}
	v, err := desc.Decode(settings.Row{Value: raw})
	if err != nil {
		r.warnOnce("decode:"+name, "user pref undecodable; using default", "key", name, "error", err)
		return desc.Default()
	}
	if err := desc.Validate(v); err != nil {
		r.warnOnce("invalid:"+name, "user pref invalid; using default", "key", name, "error", err)
		return desc.Default()
	}
	return v
}

// ApplyPartial merges a partial JSON document onto ONE key's current
// value and returns the rewritten blob. Fields the document omits keep
// their current (or default) values; every other key's raw sub-document
// is preserved byte-identical; the merged value is validated before
// anything is written. An unknown key is refused.
func (r *Registry) ApplyPartial(prefsJSON, key string, partial json.RawMessage) (string, error) {
	desc, ok := r.byName[key]
	if !ok {
		return "", invalidf("unknown user setting key %q", key)
	}
	if err := settings.RequireNonEmptyPartial(key, partial); err != nil {
		return "", err
	}
	doc, err := decodeBlob(prefsJSON)
	if err != nil {
		return "", err
	}
	// Merge onto the SAME base the read path serves. decodeOne degrades a
	// stored sub-document its own validator refuses to the key's default,
	// so decoding the raw document here instead would refuse every write to
	// a key whose stored value went bad -- the account could see the
	// default and still be unable to change it.
	raw, present := doc[key]
	current := r.decodeOne(key, raw, present)
	merged, err := desc.ApplyPartial(current, partial)
	if err != nil {
		return "", invalidf("merge partial document for %q: %w", key, err)
	}
	if err := desc.Validate(merged); err != nil {
		return "", invalidf("invalid value for %q: %w", key, err)
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("encode value for %q: %w", key, err)
	}
	doc[key] = encoded
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// Reset removes one key's sub-document from the blob, returning it to
// its default. Removing the last key leaves an empty object document.
func (r *Registry) Reset(prefsJSON, key string) (string, error) {
	if _, ok := r.byName[key]; !ok {
		return "", invalidf("unknown user setting key %q", key)
	}
	doc, err := decodeBlob(prefsJSON)
	if err != nil {
		return "", err
	}
	delete(doc, key)
	out, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// warnOnce logs at warn level the first time tag is seen.
//
// Once per PROCESS per key, and deliberately not re-armed. The instance
// scope re-arms its equivalent because it holds ONE snapshot, so "the
// value became healthy" is a fact about the whole process. Here the value
// is per user and Decode runs per request, so a healthy decode says
// nothing about the user whose document is corrupt: re-arming made one
// bad row log on every request that any other user made. The cost of the
// simpler rule is that a SECOND user corrupting the same key logs
// nothing; the first warning is what an operator acts on, and the
// per-user detail is not available here — Decode sees a blob, not an
// account.
func (r *Registry) warnOnce(tag, msg string, args ...any) {
	r.mu.Lock()
	first := !r.warned[tag]
	if first {
		r.warned[tag] = true
	}
	r.mu.Unlock()
	if first {
		slog.Warn(msg, args...)
	}
}
