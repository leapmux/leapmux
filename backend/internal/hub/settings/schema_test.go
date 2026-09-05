package settings_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/usersettings"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// knownCustomEditors is the client-side editor id set (Phase 4's
// CUSTOM_EDITORS table); a FieldCustom CustomID outside it would render
// nothing in the preferences dialog.
var knownCustomEditors = map[string]bool{
	"keybindings":    true,
	"terminalTheme":  true,
	"syntaxTheme":    true,
	"theme":          true,
	"networkAccess":  true,
	"trustedProxies": true,
}

// allDescriptors gathers BOTH descriptor sets the settings surface
// renders: the instance-scope keys (via the same registry the hub, admin
// CLI, and service tests construct) and the account-scope keys.
func allDescriptors(t *testing.T) []settings.Descriptor {
	t.Helper()
	m := settingsregistry.NewManager(nil, nil)
	require.NotEmpty(t, m.Registered())
	return append(m.Registered(), usersettings.Default.Descriptors()...)
}

// jsonFields walks a struct type's exported fields INCLUDING anonymous
// embedded structs (AltchaRow embeds AltchaSettings), returning every
// JSON-tagged field name in declaration order.
func jsonFields(t *testing.T, typ reflect.Type) []string {
	t.Helper()
	var out []string
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		tag, ok := f.Tag.Lookup("json")
		if !ok {
			// Anonymous embedded structs without their own tag contribute
			// their fields (JSON promotion).
			if f.Anonymous && f.Type.Kind() == reflect.Struct {
				out = append(out, jsonFields(t, f.Type)...)
			}
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// isWholeValueCustom reports whether `fields` is the one-field declaration a
// key uses when the client owns the editor for the entire value: a single
// FieldCustom with no name, so it addresses the key rather than a property of
// it. `theme`, `terminal_theme` and `keybindings` are the three.
func isWholeValueCustom(fields []settings.Field) bool {
	return len(fields) == 1 && fields[0].Kind == settings.FieldCustom && fields[0].Name == ""
}

// kindMatches reports whether a Field's UI kind can edit a Go value of
// the given type. Strings may be FieldString or FieldEnum; a []string is
// FieldStringList, a []byte is FieldBytes, and anything else a struct or
// slice field might carry is FieldCustom (the client owns the editor).
func kindMatches(k settings.FieldKind, typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool:
		return k == settings.FieldBool
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return k == settings.FieldInt
	case reflect.Float32, reflect.Float64:
		return k == settings.FieldFloat
	case reflect.String:
		return k == settings.FieldString || k == settings.FieldEnum
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return k == settings.FieldBytes
		}
		if typ.Elem().Kind() == reflect.String {
			return k == settings.FieldStringList
		}
		return k == settings.FieldStringList || k == settings.FieldCustom
	default:
		return k == settings.FieldCustom
	}
}

// TestSchemaMatchesValueTypes is the tripwire that makes a missing or
// stale field schema impossible: every descriptor's Fields must exactly
// cover the JSON shape of its value TYPE — not of its marshalled default,
// which omitempty would truncate (SMTPValue's default marshals without
// host or password).
//
// Driven by reflection over reflect.TypeOf(desc.Default()) so omitempty,
// embedded structs, scalars, and slices are all handled uniformly.
func TestSchemaMatchesValueTypes(t *testing.T) {
	for _, desc := range allDescriptors(t) {
		desc := desc
		t.Run(desc.Name(), func(t *testing.T) {
			ui := desc.UI()
			require.NotEmpty(t, ui.Category, "category is required")
			require.NotEmpty(t, ui.Title, "title is required")
			require.NotEmpty(t, ui.Fields, "a descriptor with no fields cannot be edited")
			// ListSettings drops a hidden key entirely, in the dialog AND in
			// `leapmux control admin settings`. A key hidden in solo and in hub
			// is therefore administrable nowhere, which no declaration can
			// mean on purpose.
			require.False(t, ui.HiddenInSolo && ui.HiddenInHub,
				"hidden in solo AND in hub leaves the key administrable in no deployment")

			typ := reflect.TypeOf(desc.Default())
			if typ == nil {
				t.Fatalf("descriptor has no default; the schema walk needs a value type")
			}
			for _, f := range ui.Fields {
				if f.Min != nil && f.Max != nil {
					require.LessOrEqual(t, *f.Min, *f.Max, "field %q: Min exceeds Max", f.Name)
				}
				if f.MinF != nil && f.MaxF != nil {
					require.LessOrEqual(t, *f.MinF, *f.MaxF, "field %q: MinF exceeds MaxF", f.Name)
				}
				if f.Kind == settings.FieldEnum {
					require.NotEmpty(t, f.EnumValues, "field %q: an enum needs values", f.Name)
					seen := map[string]bool{}
					for _, ev := range f.EnumValues {
						require.False(t, seen[ev.Value], "field %q: duplicate enum value %q", f.Name, ev.Value)
						seen[ev.Value] = true
					}
				}
				if f.Kind == settings.FieldCustom {
					require.NotEmpty(t, f.CustomID, "field %q: FieldCustom needs a CustomID", f.Name)
					require.True(t, knownCustomEditors[f.CustomID],
						"field %q: CustomID %q has no client-side editor", f.Name, f.CustomID)
				} else {
					require.Empty(t, f.CustomID, "field %q: CustomID is FieldCustom-only", f.Name)
				}
				if f.DependsOn != nil {
					assertDependsOnResolves(t, desc, f)
				}
			}

			// Secret agreement, both directions.
			secretDeclared := map[string]bool{}
			for _, name := range desc.SecretFieldNames() {
				secretDeclared[name] = true
			}
			secretInFields := map[string]bool{}
			for _, f := range ui.Fields {
				if f.Secret {
					secretInFields[f.Name] = true
					require.True(t, secretDeclared[f.Name],
						"field %q is Secret but the key does not list it in SecretFieldNames", f.Name)
					require.NotEmpty(t, f.Name, "a scalar key cannot carry a secret field")
				}
			}
			for name := range secretDeclared {
				require.True(t, secretInFields[name],
					"SecretFieldNames lists %q but no Field declares it Secret", name)
			}

			// The schema/value-shape agreement.
			switch typ.Kind() {
			case reflect.Struct:
				// A struct whose ONE field is an unnamed FieldCustom is a
				// whole-value custom editor -- the same shape a slice-shaped key
				// takes below, and checked the same way. The per-field rule under
				// it exists so a struct edited field-by-field cannot misdeclare
				// or omit a field; a key whose client owns the entire editor
				// declares no per-field schema to get wrong.
				if isWholeValueCustom(ui.Fields) {
					assert.True(t, kindMatches(settings.FieldCustom, typ),
						"kind %s cannot edit a %s", ui.Fields[0].Kind, typ)
					break
				}
				want := jsonFields(t, typ)
				got := make([]string, 0, len(ui.Fields))
				for _, f := range ui.Fields {
					got = append(got, f.Name)
				}
				assert.ElementsMatch(t, want, got,
					"Fields must be exactly the value type's JSON-tagged fields")
				for _, f := range ui.Fields {
					var goType reflect.Type
					if f.Name == "" {
						t.Fatalf("struct-shaped key %q cannot carry a scalar Field", desc.Name())
					}
					goType = fieldTypeByName(t, typ, f.Name)
					require.NotNil(t, goType, "field %q does not exist on the value type", f.Name)
					assert.True(t, kindMatches(f.Kind, goType),
						"field %q: kind %s cannot edit a %s", f.Name, f.Kind, goType)
				}
			case reflect.Slice:
				require.Len(t, ui.Fields, 1, "a slice-shaped key is one whole Field")
				f := ui.Fields[0]
				require.Empty(t, f.Name, "a slice-shaped key's Field carries the whole value")
				assert.True(t, kindMatches(f.Kind, typ),
					"kind %s cannot edit a %s", f.Kind, typ)
			default:
				require.Len(t, ui.Fields, 1, "a scalar key is one whole Field")
				f := ui.Fields[0]
				require.Empty(t, f.Name, "a scalar key's Field carries the whole value")
				assert.True(t, kindMatches(f.Kind, typ),
					"kind %s cannot edit a %s", f.Kind, typ)
			}
		})
	}
}

// fieldTypeByName resolves one JSON tag name to its Go field type,
// descending through anonymous embedded structs (JSON promotion).
func fieldTypeByName(t *testing.T, typ reflect.Type, name string) reflect.Type {
	t.Helper()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag, ok := f.Tag.Lookup("json")
		if ok {
			tagName, _, _ := strings.Cut(tag, ",")
			if tagName == name {
				return f.Type
			}
			continue
		}
		if f.Anonymous && f.Type.Kind() == reflect.Struct {
			if got := fieldTypeByName(t, f.Type, name); got != nil {
				return got
			}
		}
	}
	return nil
}

// assertDependsOnResolves checks a field's visibility condition: a
// sibling condition gives a field of the same key; a cross-key condition
// gives a registered descriptor's scalar enum, and every In value is one
// of the target's allowed values.
func assertDependsOnResolves(t *testing.T, desc settings.Descriptor, f settings.Field) {
	t.Helper()
	cond := f.DependsOn
	if cond.Key == "" {
		require.NotEmpty(t, cond.Field, "field %q: DependsOn needs a Key or a Field", f.Name)
		var target *settings.Field
		for i := range desc.UI().Fields {
			if desc.UI().Fields[i].Name == cond.Field {
				target = &desc.UI().Fields[i]
				break
			}
		}
		require.NotNil(t, target, "field %q: DependsOn.Field %q does not exist", f.Name, cond.Field)
		require.Equal(t, settings.FieldEnum, target.Kind,
			"field %q: DependsOn target %q must be an enum", f.Name, cond.Field)
		require.NotEmpty(t, cond.In, "field %q: DependsOn.In is empty", f.Name)
		allowed := map[string]bool{}
		for _, ev := range target.EnumValues {
			allowed[ev.Value] = true
		}
		for _, v := range cond.In {
			require.True(t, allowed[v],
				"field %q: DependsOn.In value %q is not one of %q's enum values", f.Name, v, cond.Field)
		}
		return
	}
	require.NotEmpty(t, cond.In, "field %q: DependsOn.In is empty", f.Name)
	var target settings.Descriptor
	for _, d := range allDescriptors(t) {
		if d.Name() == cond.Key {
			target = d
			break
		}
	}
	require.NotNil(t, target, "field %q: DependsOn.Key %q matches no registered key", f.Name, cond.Key)
	ui := target.UI()
	require.Len(t, ui.Fields, 1, "field %q: DependsOn key %q must be scalar", f.Name, cond.Key)
	require.Empty(t, ui.Fields[0].Name, "field %q: DependsOn key %q must be scalar", f.Name, cond.Key)
	require.Equal(t, settings.FieldEnum, ui.Fields[0].Kind,
		"field %q: DependsOn key %q must be an enum", f.Name, cond.Key)
	allowed := map[string]bool{}
	for _, ev := range ui.Fields[0].EnumValues {
		allowed[ev.Value] = true
	}
	for _, v := range cond.In {
		require.True(t, allowed[v],
			"field %q: DependsOn.In value %q is not one of key %q's enum values", f.Name, v, cond.Key)
	}
}

// TestEnumDeclarationsAreSingleSourced pins the enum/validator
// single-source rule: where the allowed set and the validator are not
// literally the same slice, both must derive from the same underlying
// catalogue. The captcha provider enum derives from the provider
// registry (the same map validateSelectedProvider dispatches on), and
// the altcha algorithm enum from SupportedAltchaAlgorithms (the same
// deriveKeyFuncs map AltchaSettings.Validate dispatches on) — asserted
// here so a provider or algorithm added to one but not the other fails
// this test rather than drifting silently.
func TestEnumDeclarationsAreSingleSourced(t *testing.T) {
	for _, desc := range allDescriptors(t) {
		for _, f := range desc.UI().Fields {
			if f.Kind != settings.FieldEnum {
				continue
			}
			for _, ev := range f.EnumValues {
				// The write-path validator is the authority: every enum
				// value a descriptor advertises must pass the key's own
				// validation when written as the whole value (scalar
				// enums) — a value the validator rejects could never be
				// stored, so advertising it in the UI would be a lie.
				if len(desc.UI().Fields) == 1 && desc.UI().Fields[0].Name == "" {
					if typ := reflect.TypeOf(desc.Default()); typ.Kind() == reflect.String {
						err := desc.Validate(ev.Value)
						assert.NoError(t, err,
							"%s: enum advertises %q but the validator rejects it", desc.Name(), ev.Value)
					}
				}
			}
		}
	}
}

// TestCategoriesAreKnown pins the shared category vocabulary both scopes
// declare into, so the dialog's navigation can group rows without
// guessing at unknown categories.
func TestCategoriesAreKnown(t *testing.T) {
	known := map[string]bool{
		// instance scope
		"general": true, "signup": true, "email": true, "captcha": true,
		"rate-limits": true, "limits": true, "advanced": true, "apps": true,
		"network": true,
		// user scope
		"appearance": true, "notifications": true, "shortcuts": true,
		"desktop": true,
	}
	for _, desc := range allDescriptors(t) {
		require.True(t, known[desc.UI().Category],
			"%s: category %q is not in the shared category vocabulary", desc.Name(), desc.UI().Category)
	}
}

// unprobedBounds records the declared bounds this walk cannot probe at
// all, keyed "<setting key>.<field>#min|#max" (a scalar key's field name
// is empty, so its label is "<setting key>#min").
//
// The three ALTCHA work parameters are the whole list, and they are one
// case: their legal range is family-specific, and the declared Min/Max are
// the UNION over every family (cost spans ARGON2ID's floor of 1 and
// PBKDF2's ceiling of 1000000). A probe merges one bound onto the DEFAULT
// document, whose algorithm is PBKDF2, so it would assert a range that
// family never has. The control narrows per family through DependsOn and
// the reconciler; the validator is the authority.
var unprobedBounds = map[string]string{
	"captcha.altcha.cost#min":        "the union over the algorithm families; the default document is PBKDF2, whose floor is 10000",
	"captcha.altcha.cost#max":        "the union over the algorithm families; ARGON2ID caps cost at 64",
	"captcha.altcha.memory_cost#min": "the union over the algorithm families; ARGON2ID's floor is 8192 and PBKDF2 requires exactly 0",
	"captcha.altcha.memory_cost#max": "the union over the algorithm families; PBKDF2 requires exactly 0",
	"captcha.altcha.parallelism#min": "the union over the algorithm families; SCRYPT's floor is 1 and PBKDF2 requires exactly 0",
	"captcha.altcha.parallelism#max": "the union over the algorithm families; PBKDF2 requires exactly 0",
}

// boundsWithoutOutsideRefusal records the bounds whose own value is
// storable but whose neighbour just outside is storable TOO, so the
// negative half of the probe cannot run. Each entry is a declared bound
// that is deliberately NARROWER than the validator, not a lie.
var boundsWithoutOutsideRefusal = map[string]string{
	"smtp.port#min":                       "validateSMTP returns early while the host is empty, and the default document has no host, so no port is refused there",
	"smtp.port#max":                       "validateSMTP returns early while the host is empty, and the default document has no host, so no port is refused there",
	"captcha.recaptcha_v3.min_score#minf": "the validator's interval is HALF-OPEN, (0, 1]. MinF can only express a closed floor, so the declared floor is the smallest step the control offers and every value between 0 and it is still storable",
	"limits.max_connections_per_user#min": "the rule is a UNION, 0 or at least 4, which Min cannot express; the Help text carries the part the control cannot",
	"limits.max_workers_per_user#min":     "the rule is a UNION, 0 or at least 1, which Min cannot express; the Help text carries the part the control cannot",
	"queue_budget.relay_bytes#min":        "the rule is a UNION, 0 or at least the class floor, which Min cannot express; the Help text carries the part the control cannot",
	"queue_budget.worker_bytes#min":       "the rule is a UNION, 0 or at least the class floor, which Min cannot express; the Help text carries the part the control cannot",
	"queue_budget.userevents_bytes#min":   "the rule is a UNION, 0 or at least the class floor, which Min cannot express; the Help text carries the part the control cannot",
}

// boundProbe is one declared bound rendered as the two values the walk
// asserts on: the bound itself, which the validator must ACCEPT, and its
// neighbour just outside, which it must REFUSE.
type boundProbe struct {
	label   string
	at      any
	outside any
}

// fieldBoundProbes lists every declared bound of one field.
func fieldBoundProbes(key string, f settings.Field) []boundProbe {
	label := key
	if f.Name != "" {
		label = key + "." + f.Name
	}
	var out []boundProbe
	if f.Min != nil {
		out = append(out, boundProbe{label + "#min", *f.Min, *f.Min - 1})
	}
	if f.Max != nil {
		out = append(out, boundProbe{label + "#max", *f.Max, *f.Max + 1})
	}
	// A float bound's neighbour is one small step past the edge; the
	// declared bounds are score thresholds, not exact representable edges,
	// so a nextafter-sized step would only assert floating point behaviour.
	const floatStep = 1e-6
	if f.MinF != nil {
		out = append(out, boundProbe{label + "#minf", *f.MinF, *f.MinF - floatStep})
	}
	if f.MaxF != nil {
		out = append(out, boundProbe{label + "#maxf", *f.MaxF, *f.MaxF + floatStep})
	}
	return out
}

// validateWithField resolves what the key's validator says about a value
// that sets ONE field to v, leaving every sibling at the code default.
//
// A scalar key IS the value. An object-shaped key travels through
// ApplyPartial, which is the same merge the write path runs, so the probe
// asserts what an operator editing that one control would actually store.
func validateWithField(desc settings.Descriptor, fieldName string, v any) error {
	if fieldName == "" {
		return desc.Validate(v)
	}
	partial, err := json.Marshal(map[string]any{fieldName: v})
	if err != nil {
		return err
	}
	merged, err := desc.ApplyPartial(desc.Default(), partial)
	if err != nil {
		return err
	}
	return desc.Validate(merged)
}

// TestDeclaredFieldBoundsAreLegalValues probes EVERY declared numeric
// bound -- Min, Max, MinF, MaxF, on every field of every key in both
// scopes -- against that key's own validator.
//
// `Field.Min`/`Max` exist so a control never offers a value the hub
// refuses. A bound the validator rejects is therefore a lie the operator
// pays for: the timeouts key advertised an unbounded ceiling that the wire
// truncated, and the reCAPTCHA score advertised a floor of 0.00 that the
// validator refused on every write.
//
// The walk used to cover SCALAR INT keys only, which is exactly where
// those two defects hid. Two exemption tables carry the cases a walk
// cannot judge, each with the reason: unprobedBounds (the bound is a union
// over a sibling field's value) and boundsWithoutOutsideRefusal (the bound
// is deliberately narrower than the validator).
func TestDeclaredFieldBoundsAreLegalValues(t *testing.T) {
	probed := 0
	for _, desc := range allDescriptors(t) {
		for _, f := range desc.UI().Fields {
			for _, probe := range fieldBoundProbes(desc.Name(), f) {
				if why, exempt := unprobedBounds[probe.label]; exempt {
					require.NotEmpty(t, why, "%s: an exemption needs a reason", probe.label)
					continue
				}
				probed++
				assert.NoErrorf(t, validateWithField(desc, f.Name, probe.at),
					"%s advertises %v but its validator refuses that value", probe.label, probe.at)
				if why, exempt := boundsWithoutOutsideRefusal[probe.label]; exempt {
					require.NotEmpty(t, why, "%s: an exemption needs a reason", probe.label)
					continue
				}
				assert.Errorf(t, validateWithField(desc, f.Name, probe.outside),
					"%s advertises %v as the edge but its validator accepts %v", probe.label, probe.at, probe.outside)
			}
		}
	}
	require.NotZero(t, probed, "no declared bound was probed; the walk proved nothing")
}

// TestBoundExemptionsAreLive keeps the two exemption tables honest: an
// entry whose bound no longer exists is a stale claim that would hide the
// next real defect at that label.
func TestBoundExemptionsAreLive(t *testing.T) {
	declared := map[string]bool{}
	for _, desc := range allDescriptors(t) {
		for _, f := range desc.UI().Fields {
			for _, probe := range fieldBoundProbes(desc.Name(), f) {
				declared[probe.label] = true
			}
		}
	}
	for label := range unprobedBounds {
		assert.Truef(t, declared[label], "unprobedBounds exempts %q, which no key declares", label)
	}
	for label := range boundsWithoutOutsideRefusal {
		assert.Truef(t, declared[label], "boundsWithoutOutsideRefusal exempts %q, which no key declares", label)
	}
}

// TestUIIsADeepCopy pins the hand-out rule for the presentation metadata.
// A key is a package-level value shared by every reader for the process,
// and the wire mapper assigns the field pointers and slices straight into
// a proto message, so handing out the declaration's own memory would let
// one consumer's write change what every later reader sees.
func TestUIIsADeepCopy(t *testing.T) {
	key := settings.NewKey[struct {
		Mode  string `json:"mode"`
		Limit int64  `json:"limit"`
	}]("test.uicopy").
		WithUI(settings.UIMeta{
			Category: "general",
			Title:    "Copy",
			Fields: []settings.Field{
				{Name: "mode", Label: "Mode", Kind: settings.FieldEnum,
					EnumValues: []settings.EnumValue{{Value: "a"}, {Value: "b"}}},
				{Name: "limit", Label: "Limit", Kind: settings.FieldInt,
					Min:       ptrconv.Ptr[int64](1),
					Max:       ptrconv.Ptr[int64](9),
					MinF:      ptrconv.Ptr(0.5),
					MaxF:      ptrconv.Ptr(1.5),
					DependsOn: &settings.FieldCondition{Field: "mode", In: []string{"a"}}},
			},
		})

	got := key.UI()
	got.Fields[0].Label = "vandalised"
	got.Fields[0].EnumValues[0].Value = "vandalised"
	*got.Fields[1].Min = 99
	*got.Fields[1].Max = 99
	*got.Fields[1].MinF = 99
	*got.Fields[1].MaxF = 99
	got.Fields[1].DependsOn.In[0] = "vandalised"
	got.Fields[1].DependsOn.Field = "vandalised"

	fresh := key.UI()
	assert.Equal(t, "Mode", fresh.Fields[0].Label)
	assert.Equal(t, "a", fresh.Fields[0].EnumValues[0].Value)
	assert.Equal(t, int64(1), *fresh.Fields[1].Min)
	assert.Equal(t, int64(9), *fresh.Fields[1].Max)
	assert.InDelta(t, 0.5, *fresh.Fields[1].MinF, 0)
	assert.InDelta(t, 1.5, *fresh.Fields[1].MaxF, 0)
	assert.Equal(t, []string{"a"}, fresh.Fields[1].DependsOn.In)
	assert.Equal(t, "mode", fresh.Fields[1].DependsOn.Field)
}

// TestSecretFieldNamesIsACopy pins the same rule for the secret list: a
// caller that sorted or appended to it would change what every later
// reader of the key sees.
func TestSecretFieldNamesIsACopy(t *testing.T) {
	key := settings.NewKey[struct {
		A string `json:"a"`
		B string `json:"b"`
	}]("test.secretcopy").SecretFields("a", "b")

	got := key.SecretFieldNames()
	got[0] = "vandalised"
	assert.Equal(t, []string{"a", "b"}, key.SecretFieldNames())
}

// TestDefaultIsACopy pins the hand-out rule for the default value. The
// snapshot stores Default() on BOTH degrade paths, so an uncopied slice or
// map would put the package-level default itself into process-wide state,
// where the next consumer's append rewrites it for everyone.
func TestDefaultIsACopy(t *testing.T) {
	type listValue struct {
		Items []string          `json:"items"`
		Tags  map[string]string `json:"tags"`
	}
	key := settings.NewKey[listValue]("test.defaultcopy").
		WithDefault(listValue{Items: []string{"one"}, Tags: map[string]string{"k": "v"}})

	got, ok := key.Default().(listValue)
	require.True(t, ok)
	got.Items[0] = "vandalised"
	got.Tags["k"] = "vandalised"

	fresh, ok := key.Default().(listValue)
	require.True(t, ok)
	assert.Equal(t, []string{"one"}, fresh.Items)
	assert.Equal(t, map[string]string{"k": "v"}, fresh.Tags)
}
