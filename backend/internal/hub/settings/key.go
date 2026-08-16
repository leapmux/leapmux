// Package settings provides the hub's instance-level runtime configuration,
// stored in the hub_settings table as one row per setting key. Each key is
// declared in Go as a typed handle (a Key[T]) carrying its JSON shape,
// default, validators, and propagation class; adding, removing, or
// reshaping a setting is a code change only, never a schema migration.
//
// The Manager caches a decoded Snapshot of every registered key with a
// short TTL (mirroring the session and captcha caches), so admin CLI
// writes propagate to a running hub within the TTL. Keys marked
// PropagationRestart are consumed once at startup instead — pool floors
// and protocol ceilings are computed from them before any request is
// served, so a restart is the only safe way to apply them.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	// PropagationRestart means the value is read once at startup; a
	// change takes effect on the next restart. Used for values that feed
	// startup-time arithmetic (queue pool floors, frame ceilings) where a
	// mid-flight change could violate an invariant the pools rely on.
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
// admin CLI can do with a registered key without knowing its type.
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
	// halves over the default gives partial-row semantics for free: a
	// field the stored document omits keeps the default, and the secret
	// half fills exactly the fields it names.
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
	// Doc returns the one-line summary and JSON shape hint the admin CLI
	// renders for the key. Either may be empty; the CLI falls back to a
	// type-derived shape and no summary.
	Doc() (summary, shape string)
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
	validate    func(T) error
	// secretFields are the JSON field names stored in the encrypted half.
	secretFields []string
	// doc is the (summary, shape) pair the admin CLI renders.
	summary, shape string
}

// NewKey declares a setting. The name is the hub_settings key and should
// be dot-namespaced by domain ("smtp", "rate_limit.change-password",
// "captcha.altcha").
func NewKey[T any](name string) *Key[T] {
	return &Key[T]{name: name}
}

// WithDefault sets the value an absent row resolves to. Without it the
// zero value of T is the default.
func (k *Key[T]) WithDefault(v T) *Key[T] {
	k.def = v
	return k
}

// WithValidate attaches the merged-value validator the Manager's write
// path enforces (and the read path degrades on).
func (k *Key[T]) WithValidate(fn func(T) error) *Key[T] {
	k.validate = fn
	return k
}

// SecretFields names the JSON fields of T that live in the row's
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

// WithDoc attaches the one-line summary and JSON shape hint the admin
// CLI renders for the key, so a key's documentation lives in its single
// declaration beside its default and validators rather than in a parallel
// CLI-side registry a new key must remember to edit.
func (k *Key[T]) WithDoc(summary, shape string) *Key[T] {
	k.summary, k.shape = summary, shape
	return k
}

func (k *Key[T]) Name() string                 { return k.name }
func (k *Key[T]) Propagation() Propagation     { return k.propagation }
func (k *Key[T]) HasSecret() bool              { return len(k.secretFields) > 0 }
func (k *Key[T]) SecretFieldNames() []string   { return k.secretFields }
func (k *Key[T]) Default() any                 { return k.def }
func (k *Key[T]) Doc() (summary, shape string) { return k.summary, k.shape }

// Decode implements Descriptor: defaults first, then the public half, then
// the secret half — each unmarshal only touches the fields its document
// names, so a partial stored row keeps the defaults for the rest.
func (k *Key[T]) Decode(row Row) (any, error) {
	v := k.def
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
// overlay the current value's; omitted fields are untouched. Unknown
// field names are refused — a partial document whose every field name
// misses (a one-character typo) would otherwise merge to the unchanged
// value and report success while changing nothing.
func (k *Key[T]) ApplyPartial(v any, partial json.RawMessage) (any, error) {
	typed, err := k.asT(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(partial))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&typed); err != nil {
		return nil, err
	}
	return typed, nil
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
	return k.def
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
