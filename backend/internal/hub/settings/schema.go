package settings

import "slices"

// The declarative field schema every key's UIMeta carries. A key's UI
// metadata used to live in two places — WithDoc's summary for the CLI and
// a hypothetical per-dialog declaration for the frontend — which would
// drift. UIMeta is the one declaration: the CLI's `settings get` renders
// Title/Summary plus a Fields-derived shape hint, the preferences dialog
// renders Title/Summary/Fields, and the schema test walks every
// descriptor's value TYPE against its Fields so a value shape change
// without a matching schema change cannot compile silently past the test.

// FieldKind is the UI kind of one field. The set covers what the real
// values are, not a convenient subset: min_score is float64, the font
// lists are []string, the ALTCHA HMAC key is []byte, and keybindings need
// an editor only the client can provide.
type FieldKind int

const (
	FieldBool FieldKind = iota
	// FieldInt is a 64-bit-or-narrower integer (int or int64 in Go).
	FieldInt
	// FieldFloat is a float64 rendered with a fractional step (the
	// reCAPTCHA score threshold).
	FieldFloat
	FieldString
	FieldEnum
	// FieldStringList is an ordered list of short strings (the font
	// stacks).
	FieldStringList
	// FieldBytes is a binary secret (base64 in JSON, stored in the
	// encrypted half): write-only from the UI's perspective.
	FieldBytes
	// FieldCustom is an opaque value whose editor the client owns; the
	// CustomID identifies it. The generic widget layer renders nothing for
	// it.
	FieldCustom
)

// String renders the kind for diagnostics and the CLI's shape hints.
func (k FieldKind) String() string {
	switch k {
	case FieldBool:
		return "bool"
	case FieldInt:
		return "integer"
	case FieldFloat:
		return "float"
	case FieldString:
		return "string"
	case FieldEnum:
		return "enum"
	case FieldStringList:
		return "string-list"
	case FieldBytes:
		return "bytes"
	case FieldCustom:
		return "custom"
	default:
		return "unknown"
	}
}

// EnumValue is one allowed value of a FieldEnum field.
type EnumValue struct {
	Value string
	Label string
	Help  string
}

// EnumAllowed reports whether v is one of the enum's declared values. The
// declaration is the one source of the allowed set, so a validator that
// needs its own message (the SMTP TLS mode) asks here rather than
// restating the list.
func EnumAllowed(values []EnumValue, v string) bool {
	for _, ev := range values {
		if ev.Value == v {
			return true
		}
	}
	return false
}

// FieldCondition makes one field's visibility depend on another: the
// field renders only when the field it points at currently holds one of
// In. Key is the settings key the condition reads; "" means a sibling
// field of the same key (Field identifies the sibling).
type FieldCondition struct {
	Key   string
	Field string
	In    []string
}

// Field is one editable piece of a setting's value. For scalar-shaped
// keys (bool, int64, string, enums) there is exactly one Field with
// Name "" — the key itself is the scalar. For object-shaped keys there
// is one Field per JSON-tagged field of the value type, and the schema
// test enforces the correspondence by reflection.
type Field struct {
	// Name is the JSON tag the field edits; "" when the key itself is
	// the scalar.
	Name  string
	Label string
	Help  string
	Kind  FieldKind
	// EnumValues lists the allowed values of a FieldEnum field. The
	// enum's allowed set must have exactly one source: either the key's
	// validator is derived from this slice, or both are derived from the
	// same underlying catalogue (see the captcha provider registry and
	// SupportedAltchaAlgorithms). Declarations note which.
	EnumValues []EnumValue
	Min, Max   *int64
	MinF, MaxF *float64
	// Unit is "", "seconds", "bytes", "score", or "count" — rendered
	// beside numeric inputs and used by the CLI's shape hints.
	Unit        string
	Secret      bool
	Placeholder string
	// DependsOn hides this field unless the condition holds (see
	// FieldCondition).
	DependsOn *FieldCondition
	// CustomID identifies the client-side editor for a FieldCustom field
	// (e.g. "keybindings"). The schema test asserts it is a known id.
	CustomID string
}

// clone deep-copies one field: the enum list, the four bound pointers, and
// the visibility condition with its own value list.
func (f Field) clone() Field {
	out := f
	out.EnumValues = slices.Clone(f.EnumValues)
	out.Min = clonePtr(f.Min)
	out.Max = clonePtr(f.Max)
	out.MinF = clonePtr(f.MinF)
	out.MaxF = clonePtr(f.MaxF)
	if f.DependsOn != nil {
		cond := *f.DependsOn
		cond.In = slices.Clone(f.DependsOn.In)
		out.DependsOn = &cond
	}
	return out
}

// clonePtr returns a fresh pointer holding the same value, or nil.
func clonePtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// UIMeta is a key's presentation: where it sits in the settings surface,
// what it is called, and the editable shape of its value. Categories are
// shared between the instance scope (hub settings) and the user scope
// (account settings) so the preferences dialog can interleave both.
type UIMeta struct {
	// Category is one of "general", "signup", "email", "captcha",
	// "rate-limits", "limits" (instance scope) or "appearance",
	// "notifications", "shortcuts", "desktop" (user scope). "advanced" is
	// declared in BOTH scopes, and the dialog renders one navigation group per
	// scope.
	Category string
	Title    string
	// Summary is the one-line description the CLI's `settings get` and
	// the dialog's help text render — the successor of WithDoc's summary.
	Summary string
	// HiddenInSolo omits the key from solo deployments' settings surface:
	// a single-user hub has no sign-up, no captcha, no per-user rate
	// limits, no session or cookie, and no way to send mail.
	//
	// The flag is per KEY, and it is the ONLY hiding mechanism. A whole
	// section disappears when every key in its category carries the flag,
	// because occupiedNavGroups (frontend) drops a navigation group whose
	// rows are all hidden. So do not reason about a category: decide each
	// key on whether solo reads it. The general category is the case that
	// proves the difference -- public_url stays live in solo, so that
	// section survives with one row while its two siblings hide.
	//
	// It hides more than the dialog. ListSettings omits the key in solo,
	// which also takes it out of `leapmux control admin settings`. Marking a key
	// that solo still reads makes it unadministrable in BOTH surfaces.
	HiddenInSolo bool
	Fields       []Field
}

// clone deep-copies the metadata so a reader can never reach the
// declaration's own slices and pointers. Key.UI hands one out per call.
func (u UIMeta) clone() UIMeta {
	out := u
	if u.Fields == nil {
		return out
	}
	out.Fields = make([]Field, len(u.Fields))
	for i, f := range u.Fields {
		out.Fields[i] = f.clone()
	}
	return out
}
