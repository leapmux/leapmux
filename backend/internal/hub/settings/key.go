// Package settings provides the hub's instance-level runtime configuration,
// stored in the hub_settings table as one row per setting key. Each key is
// declared in Go as a typed handle (a Key[T]) carrying its JSON shape,
// default, validators, and propagation class; adding, removing, or
// reshaping a setting is a code change only, never a schema migration.
//
// The Manager caches a decoded Snapshot of every registered key with a
// short TTL (mirroring the session and captcha caches), so admin CLI
// writes propagate to a running hub within the TTL. Keys marked
// PropagationRestart are consumed once at startup instead — the hub
// computes pool floors and protocol ceilings from them before it serves
// any request, so a restart is the only safe way to apply them.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
)

// Propagation classifies how a running hub applies changes to a key.
type Propagation int

const (
	// PropagationHot means the hub applies the new value within the
	// snapshot TTL (set by how the value is consumed — a cookie name
	// or session duration applies to the next request, a pool budget
	// cannot apply at all).
	//
	// Hot keys are consumed one way: each consumer re-resolves Snapshot
	// per use, holding no lock of its own. The one exception is the
	// per-user limits: their read paths hold locks that must not take a
	// settings read (the auth registry reads its cap inside the
	// revocation critical section; the registration path reads it inside
	// a store transaction), so the hub pushes changes into their
	// pre-existing atomic setters via Manager.Subscribe — see
	// backend/hub/server.go. A new key follows the re-resolve idiom
	// unless its consumer cannot take a settings read; push is the
	// exception, not a second default.
	PropagationHot Propagation = iota
	// PropagationRestart means the hub reads the value once at startup; a
	// change takes effect on the next restart. It applies to values that
	// feed startup-time arithmetic (queue pool floors, frame ceilings)
	// where a mid-flight change could violate an invariant the pools rely
	// on.
	PropagationRestart
)

// String renders the class for logs and the admin CLI.
func (p Propagation) String() string {
	switch p {
	case PropagationRestart:
		return "restart"
	default:
		return "hot"
	}
}

// Row is one stored setting's decoded halves: raw JSON documents. Either
// half may be nil when the row (or that column) is absent. The secret half
// arrives already decrypted; encryption is the Manager's write-path
// concern, not the decode concern.
type Row struct {
	Value  json.RawMessage
	Secret json.RawMessage
}

// Descriptor is the non-generic face of a Key[T]: what the Manager and the
// admin surfaces can do with a registered key without knowing its type.
type Descriptor interface {
	// Name is the hub_settings key.
	Name() string
	// Propagation reports how changes apply to a running hub.
	Propagation() Propagation
	// HasSecret reports whether the key's shape includes secret fields
	// (stored in the encrypted half).
	HasSecret() bool
	// SecretFieldNames lists the JSON field names stored in the
	// encrypted half (empty when the key carries no secret).
	SecretFieldNames() []string
	// Decode merges a stored row onto the key's default. Unmarshaling the
	// halves over the default gives partial-row semantics with no extra
	// work: a field the stored document omits keeps the default, and the
	// secret half fills exactly the fields it specifies.
	Decode(row Row) (any, error)
	// Validate checks a fully-merged value.
	Validate(v any) error
	// ApplyPartial unmarshals a partial JSON document onto a decoded
	// value, returning the merged value. A typed method rather than a
	// plain json.Unmarshal at the call site: unmarshaling into an `any`
	// that holds a struct REPLACES it with a map instead of merging, so
	// the concrete type must be recovered before the decode.
	ApplyPartial(v any, partial json.RawMessage) (any, error)
	// Split separates a merged value into its public and secret JSON
	// documents for storage.
	Split(v any) (public, secret json.RawMessage, err error)
	// Redacted returns the value with secret fields replaced, for display
	// and logging.
	Redacted(v any) any
	// Default returns the key's default value.
	Default() any
	// UI returns the key's presentation metadata: category, title,
	// summary, and the editable field schema. Every key declares one;
	// the schema test refuses a descriptor whose UI does not match its
	// value type.
	UI() UIMeta
}

// Key is a typed setting handle: the single declaration of one setting's
// name, shape, default, and rules. Keys are package-level values owned by
// the package that consumes the setting (the SMTP key in this package,
// rate-limit keys in internal/hub/ratelimit, captcha keys in
// internal/hub/captcha), which keeps domain knowledge in its domain and
// this package generic.
//
// Keys are built with the fluent constructor: NewKey[SMTPValue]("smtp").
// Default(...).Validate(...).SecretFields("password"). The methods mutate
// and return the handle so the whole declaration reads as one statement.
type Key[T any] struct {
	name        string
	propagation Propagation
	def         T
	// defJSON is the default's JSON encoding, computed once when the
	// default is declared. Every copy of the default -- Decode's merge
	// base, Default, Of's miss -- unmarshals it, which is half the work
	// isolate would repeat on each call.
	defJSON   json.RawMessage
	validate  func(T) error
	normalize func(prev, next T, specified map[string]bool) T
	// secretFields are the JSON field names stored in the encrypted half.
	secretFields []string
	// ui is the presentation metadata (category, title, summary, field
	// schema) the admin surfaces render.
	ui UIMeta
}

// NewKey declares a setting. The name is the hub_settings key and should
// be dot-namespaced by domain ("smtp", "rate_limit.elevation",
// "captcha.altcha").
func NewKey[T any](name string) *Key[T] {
	k := &Key[T]{name: name}
	var zero T
	k.setDefault(zero)
	return k
}

// WithDefault sets the value an absent row resolves to. Without it the
// zero value of T is the default.
func (k *Key[T]) WithDefault(v T) *Key[T] {
	k.setDefault(v)
	return k
}

// setDefault records the default and its JSON encoding together. Both
// callers run at declaration time, on one goroutine, so defJSON needs no
// lock and no lazy fill.
func (k *Key[T]) setDefault(v T) {
	k.def = v
	b, err := json.Marshal(v)
	if err != nil {
		// A default that cannot marshal is a declaration bug, but it must
		// not make the key unusable at init. Leave defJSON empty; defaultCopy then
		// falls back to isolate, which reports the same failure at a call
		// site where the caller can see it.
		k.defJSON = nil
		return
	}
	k.defJSON = b
}

// defaultCopy returns a fresh copy of the default that shares no slice or
// map with it. `k.def` is a package-level value shared by every reader for
// the process, so a copy is what makes handing it out safe: without one, a
// consumer that appends to a default slice, or a decode that reuses its
// backing array, rewrites the default every later reader starts from.
func (k *Key[T]) defaultCopy() (T, error) {
	if len(k.defJSON) == 0 {
		return isolate(k.def)
	}
	var out T
	if err := json.Unmarshal(k.defJSON, &out); err != nil {
		return out, fmt.Errorf("copy value: %w", err)
	}
	return out, nil
}

// WithValidate attaches the merged-value validator the Manager's write
// path enforces (and the read path degrades on).
func (k *Key[T]) WithValidate(fn func(T) error) *Key[T] {
	k.validate = fn
	return k
}

// WithNormalize attaches a reconciler that ApplyPartial runs after it
// overlays a partial document.
//
// It exists for a value whose fields are not independent: changing one
// reinterprets the others, so a caller that writes only that one field
// leaves the document incoherent. Reconciling HERE, inside the merge, is
// what makes every client behave alike — the preferences dialog writes one
// field per row, the CLI writes several at once, and neither has to know
// the rule.
//
// The reconciler receives the value before the merge, the value after it,
// and the set of top-level field names the partial document specified.
// That last argument is the whole point: it separates "the caller left
// this field alone" from "the caller set it to its zero value", so a
// reconciler can supply a default without overwriting an explicit choice
// made in the same document.
//
// A reconciler must not validate. It runs before the validator, and a
// value it cannot reconcile is one the validator refuses with its own
// message.
//
// THE BOUNDARY: ApplyPartial is the only caller. SetValue and SetIfAbsent
// do NOT reconcile, and must not: they take a COMPLETE value in which
// every field is explicit, so the third argument would have to list every
// field, which makes the reconciler a no-op -- and an empty set would
// let it overwrite values the caller stated. A key whose fields
// depend on each other therefore reconciles at the merge, and a whole-value
// writer is responsible for handing over a coherent value.
func (k *Key[T]) WithNormalize(fn func(prev, next T, specified map[string]bool) T) *Key[T] {
	k.normalize = fn
	return k
}

// SecretFields lists the JSON fields of T that live in the row's
// encrypted half. A non-empty list makes the key secret-bearing: writes
// split those fields out for encryption, and decode merges the decrypted
// half back in. Only object-shaped values can carry secret fields.
func (k *Key[T]) SecretFields(fields ...string) *Key[T] {
	k.secretFields = append([]string(nil), fields...)
	return k
}

// Restart marks the key restart-class (default: hot).
func (k *Key[T]) Restart() *Key[T] {
	k.propagation = PropagationRestart
	return k
}

// WithUI attaches the presentation metadata — the category, title,
// summary, and editable field schema the admin surfaces render — so a
// key's documentation lives in its single declaration beside its default
// and validators rather than in a parallel client-side registry a new
// key must remember to edit.
func (k *Key[T]) WithUI(ui UIMeta) *Key[T] {
	k.ui = ui
	return k
}

func (k *Key[T]) Name() string             { return k.name }
func (k *Key[T]) Propagation() Propagation { return k.propagation }
func (k *Key[T]) HasSecret() bool          { return len(k.secretFields) > 0 }

// SecretFieldNames returns a copy: the key is a package-level value, and a
// caller that sorted or appended to the live slice would change what every
// later reader of this key sees.
func (k *Key[T]) SecretFieldNames() []string { return slices.Clone(k.secretFields) }

// Default returns a fresh copy of the key's default. Copying is a property
// of handing the value out, not a rule each caller has to remember: the
// snapshot stores this value on both degrade paths, so an uncopied slice
// or map would put the package-level default itself into process-wide
// state. A default that cannot be copied falls back to the value itself,
// which is what every caller had before.
func (k *Key[T]) Default() any {
	v, err := k.defaultCopy()
	if err != nil {
		return k.def
	}
	return v
}

// UI returns a DEEP COPY of the presentation metadata: the Fields slice,
// each field's EnumValues, its four bound pointers, and its DependsOn with
// that condition's own value list. The declaration is process-wide state,
// and the wire mapper assigns those pointers and slices straight into the
// proto message, so one consumer that wrote through them would change what
// every later reader sees.
func (k *Key[T]) UI() UIMeta { return k.ui.clone() }

// Decode implements Descriptor: defaults first, then the public half, then
// the secret half — each unmarshal only touches the fields its document
// specifies, so a partial stored row keeps the defaults for the rest.
func (k *Key[T]) Decode(row Row) (any, error) {
	// Decode onto a COPY of the default, never onto the default itself:
	// encoding/json reuses an existing slice's backing array, so decoding
	// straight onto the package-level value would let one stored row
	// rewrite the default that every later decode starts from.
	v, err := k.defaultCopy()
	if err != nil {
		return nil, err
	}
	if len(row.Value) > 0 {
		if err := json.Unmarshal(row.Value, &v); err != nil {
			return nil, fmt.Errorf("decode value half: %w", err)
		}
	}
	if len(row.Secret) > 0 {
		if err := json.Unmarshal(row.Secret, &v); err != nil {
			return nil, fmt.Errorf("decode secret half: %w", err)
		}
	}
	return v, nil
}

// asT recovers the concrete type from a value the Manager holds as `any`,
// with the one type-mismatch error shared by every typed entry point.
func (k *Key[T]) asT(v any) (T, error) {
	typed, ok := v.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("value for %q has type %T, want %T", k.name, v, zero)
	}
	return typed, nil
}

func (k *Key[T]) Validate(v any) error {
	typed, err := k.asT(v)
	if err != nil {
		return err
	}
	if k.validate == nil {
		return nil
	}
	return k.validate(typed)
}

// ApplyPartial implements Descriptor: the partial document's fields
// overlay the current value's, and omitted fields stay as they are. It
// refuses unknown field names — a partial document whose every field name
// misses (a one-character typo) would otherwise merge to the unchanged
// value and report success while changing nothing.
func (k *Key[T]) ApplyPartial(v any, partial json.RawMessage) (any, error) {
	prev, err := k.asT(v)
	if err != nil {
		return nil, err
	}
	// Merge onto an ISOLATED copy. encoding/json REUSES an existing
	// slice's backing array when it decodes into one, so decoding straight
	// onto `prev` would rewrite the very value a reconciler compares
	// against — and would reach back into whatever the caller still holds.
	next, err := isolate(prev)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(partial))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&next); err != nil {
		return nil, err
	}
	if k.normalize == nil {
		return next, nil
	}
	return k.normalize(prev, next, partialFieldNames(partial)), nil
}

// isolate returns a copy of v that shares no slice or map with it.
//
// The copy goes through the value's own JSON shape, which is the shape the
// whole settings model is defined in: Decode, Split, and ApplyPartial all
// travel as JSON, so a field that does not survive a round trip is not a
// field this package can store.
func isolate[T any](v T) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, fmt.Errorf("copy value: %w", err)
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("copy value: %w", err)
	}
	return out, nil
}

// RequireNonEmptyPartial refuses a partial document that cannot change
// anything. BOTH scopes call it, so an omitted or inert document is one
// refusal with one classification wherever it arrives.
//
// Three shapes qualify. An absent document (the proto3 field the client
// left unset arrives as "") reaches ApplyPartial as a bare EOF from the
// JSON decoder, which reads as an internal fault rather than a missing
// argument. An empty object and the literal null merge cleanly and leave
// every field as it was — and a clean merge is not a no-op: the write
// path upserts the row anyway, so the key starts reporting customized=true
// and the surface offers a reset for a change that never happened.
//
// A scalar document (true, 12, "altcha") is NOT empty: the whole value is
// what it carries.
func RequireNonEmptyPartial(key string, partial json.RawMessage) error {
	trimmed := bytes.TrimSpace(partial)
	if len(trimmed) == 0 {
		return Invalidf("settings key %q: the partial document is required", key)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &doc); err == nil && len(doc) == 0 {
		return Invalidf("settings key %q: the partial document specifies no field", key)
	}
	return nil
}

// partialFieldNames lists the top-level field names a partial document
// specifies. A scalar document specifies none, so the result is empty
// rather than an error: a scalar key has no sibling fields to reconcile.
func partialFieldNames(partial json.RawMessage) map[string]bool {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(partial, &doc); err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(doc))
	for name := range doc {
		out[name] = true
	}
	return out
}

// Split implements Descriptor: the typed delegate of splitHalves.
func (k *Key[T]) Split(v any) (json.RawMessage, json.RawMessage, error) {
	typed, err := k.asT(v)
	if err != nil {
		return nil, nil, err
	}
	return k.splitHalves(typed)
}

// Redacted implements Descriptor by copying the value's JSON object form
// and replacing each secret field with a placeholder. Scalars never carry
// secrets, so they pass through unchanged.
func (k *Key[T]) Redacted(v any) any {
	if len(k.secretFields) == 0 {
		return v
	}
	doc, err := toJSONDoc(v)
	if err != nil {
		return "<undecodable>"
	}
	for _, f := range k.secretFields {
		if _, ok := doc[f]; ok {
			doc[f] = "<redacted>"
		}
	}
	return doc
}

// Of reads the key's effective value from a snapshot. A key missing from
// the snapshot (never registered with this manager, or degraded at decode)
// resolves to the default, so a consumer never sees a zero value it cannot
// distinguish from a real one.
func (k *Key[T]) Of(s *Snapshot) T {
	if v, ok := s.values[k.name].(T); ok {
		return v
	}
	v, err := k.defaultCopy()
	if err != nil {
		return k.def
	}
	return v
}

// Set writes a fully-merged typed value through the manager, splitting
// and encrypting the secret half as needed.
func (k *Key[T]) Set(ctx context.Context, m *Manager, v T) error {
	return m.SetValue(ctx, k, v)
}

// SetIfAbsent writes the value only when the key has no row (the
// first-use provisioning primitive).
func (k *Key[T]) SetIfAbsent(ctx context.Context, m *Manager, v T) error {
	return m.SetIfAbsent(ctx, k, v)
}

// toJSONDoc marshals v and re-decodes it into a generic object so secret
// fields can be replaced without reflection.
func toJSONDoc(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// splitHalves separates a merged value into its public and secret JSON
// documents. The public document omits secret fields entirely (not merely
// zeroes them) so a decoded value never round-trips a secret through the
// unencrypted column; the secret document carries exactly the secret
// fields. A value with no secret fields keeps one whole public document.
func (k *Key[T]) splitHalves(v T) (public json.RawMessage, secret json.RawMessage, err error) {
	if len(k.secretFields) == 0 {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, nil, fmt.Errorf("marshal value: %w", err)
		}
		return b, nil, nil
	}
	doc, err := toJSONDoc(v)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal value: %w", err)
	}
	secretDoc := make(map[string]any, len(k.secretFields))
	for _, f := range k.secretFields {
		if val, ok := doc[f]; ok {
			secretDoc[f] = val
			delete(doc, f)
		}
	}
	pub, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal public half: %w", err)
	}
	sec, err := json.Marshal(secretDoc)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal secret half: %w", err)
	}
	return pub, sec, nil
}
