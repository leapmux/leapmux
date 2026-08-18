package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/settingsregistry"
	"github.com/leapmux/leapmux/internal/hub/usersettings"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// TestSettingFieldKindToProtoIsTotal keeps the kind mapping total.
//
// The default arm answers UNSPECIFIED, which is indistinguishable on the
// wire from "the hub did not say". So a FieldKind added without an arm
// here does not fail to compile and does not fail a test — every field of
// that kind simply arrives kindless, and a client renders nothing for it.
// This walks the declared kinds instead of trusting the switch.
func TestSettingFieldKindToProtoIsTotal(t *testing.T) {
	// Every kind the schema declares, in declaration order. A new kind
	// added to the iota block without an entry here fails the count check
	// below.
	kinds := []settings.FieldKind{
		settings.FieldBool,
		settings.FieldInt,
		settings.FieldFloat,
		settings.FieldString,
		settings.FieldEnum,
		settings.FieldStringList,
		settings.FieldBytes,
		settings.FieldCustom,
	}
	require.Equal(t, "unknown", settings.FieldKind(len(kinds)).String(),
		"a FieldKind was added to the iota block; add it to this walk and to settingFieldKindToProto")

	seen := map[leapmuxv1.SettingFieldKind]bool{}
	for _, k := range kinds {
		got := settingFieldKindToProto(k)
		assert.NotEqualf(t, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED, got,
			"FieldKind %s maps to UNSPECIFIED; a client cannot tell that from an absent kind", k)
		assert.Falsef(t, seen[got], "two FieldKinds map to the same proto kind (%v)", got)
		seen[got] = true
	}
}

// TestSettingDescriptorToProtoCarriesEveryDeclaredField pins that the
// mapper drops nothing a client needs to render a control the hub will
// accept: the bounds, the enum values, the secret flag, and the visibility
// condition.
func TestSettingDescriptorToProtoCarriesEveryDeclaredField(t *testing.T) {
	key := settings.NewKey[struct {
		Mode  string `json:"mode"`
		Limit int64  `json:"limit"`
		Token string `json:"token"`
	}]("test.shape").
		SecretFields("token").
		WithUI(settings.UIMeta{
			Category:     "general",
			Title:        "Shape",
			Summary:      "a fixture",
			HiddenInSolo: true,
			Fields: []settings.Field{
				{
					Name: "mode", Label: "Mode", Kind: settings.FieldEnum,
					EnumValues: []settings.EnumValue{{Value: "a", Label: "A", Help: "the a"}, {Value: "b"}},
				},
				{
					Name: "limit", Label: "Limit", Kind: settings.FieldInt, Unit: "count",
					Min:       ptrconv.Ptr[int64](1),
					Max:       ptrconv.Ptr[int64](9),
					DependsOn: &settings.FieldCondition{Field: "mode", In: []string{"a"}},
				},
				{Name: "token", Label: "Token", Kind: settings.FieldString, Secret: true, Placeholder: "paste"},
			},
		})

	got := settingDescriptorToProto(key)
	require.Equal(t, "test.shape", got.GetKey())
	assert.Equal(t, "general", got.GetCategory())
	assert.Equal(t, "Shape", got.GetTitle())
	assert.Equal(t, "a fixture", got.GetSummary())
	assert.True(t, got.GetHiddenInSolo())
	require.Len(t, got.GetFields(), 3)

	mode := got.GetFields()[0]
	assert.Equal(t, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_ENUM, mode.GetKind())
	require.Len(t, mode.GetEnumValues(), 2)
	assert.Equal(t, "a", mode.GetEnumValues()[0].GetValue())
	assert.Equal(t, "the a", mode.GetEnumValues()[0].GetHelp())

	limit := got.GetFields()[1]
	require.NotNil(t, limit.Min)
	require.NotNil(t, limit.Max)
	assert.Equal(t, int64(1), limit.GetMin())
	assert.Equal(t, int64(9), limit.GetMax())
	assert.Equal(t, "count", limit.GetUnit())
	require.NotNil(t, limit.GetDependsOn())
	assert.Equal(t, "mode", limit.GetDependsOn().GetField())
	assert.Equal(t, []string{"a"}, limit.GetDependsOn().GetIn())

	token := got.GetFields()[2]
	assert.True(t, token.GetSecret(), "a secret field must be marked, or a client renders it as plain text")
	assert.Equal(t, "paste", token.GetPlaceholder())
}

// TestSettingDescriptorToProtoDerivesRestart pins the one derived field.
// Restart does not come from UIMeta; it comes from the key's propagation
// class. A client shows the "restart required" note from this flag alone,
// so a hot key marked restart-class nags on every edit and a restart-class
// key left hot silently does not take effect.
func TestSettingDescriptorToProtoDerivesRestart(t *testing.T) {
	ui := settings.UIMeta{
		Category: "advanced", Title: "T",
		Fields: []settings.Field{{Name: "", Kind: settings.FieldInt}},
	}
	hot := settings.NewKey[int64]("test.hot").WithUI(ui)
	restart := settings.NewKey[int64]("test.restart").Restart().WithUI(ui)

	assert.False(t, settingDescriptorToProto(hot).GetRestart())
	assert.True(t, settingDescriptorToProto(restart).GetRestart())
}

// TestSettingFieldConditionCarriesTheKey pins the cross-key half of a
// visibility condition. An empty Key means "a sibling field of this key";
// dropping a NON-empty one silently re-points the condition at a sibling
// that does not exist, and the field then never renders.
func TestSettingFieldConditionCarriesTheKey(t *testing.T) {
	key := settings.NewKey[int64]("test.cond").WithUI(settings.UIMeta{
		Category: "advanced", Title: "T",
		Fields: []settings.Field{{
			Name: "", Kind: settings.FieldInt,
			DependsOn: &settings.FieldCondition{Key: "other.key", Field: "mode", In: []string{"on"}},
		}},
	})

	cond := settingDescriptorToProto(key).GetFields()[0].GetDependsOn()
	require.NotNil(t, cond)
	assert.Equal(t, "other.key", cond.GetKey())
	assert.Equal(t, "mode", cond.GetField())
	assert.Equal(t, []string{"on"}, cond.GetIn())
}

// TestEveryRegisteredDescriptorMapsWithAKind walks the REAL descriptor
// sets — both scopes — rather than a fixture, so a key declared with a
// kind the mapper does not know fails here rather than rendering blank in
// a client.
func TestEveryRegisteredDescriptorMapsWithAKind(t *testing.T) {
	m := settingsregistry.NewManager(nil, nil)
	descriptors := append(m.Registered(), usersettings.Default.Descriptors()...)
	require.NotEmpty(t, descriptors)

	checked := 0
	for _, d := range descriptors {
		got := settingDescriptorToProto(d)
		assert.Equalf(t, d.Name(), got.GetKey(), "%s: key must survive the mapping", d.Name())
		assert.NotEmptyf(t, got.GetFields(), "%s: every key declares at least one field", d.Name())
		for _, f := range got.GetFields() {
			assert.NotEqualf(t, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED, f.GetKind(),
				"%s.%s reaches the wire with no kind", d.Name(), f.GetName())
			checked++
		}
	}
	require.NotZero(t, checked, "no field was checked; the walk proved nothing")
}

// TestMarshalSettingJSONNeverReturnsEmpty pins the fallback. An empty
// string is the wire's "absent", and the frontend's parse reads it as
// undefined — so a marshal failure must not be reported as "no value".
func TestMarshalSettingJSONNeverReturnsEmpty(t *testing.T) {
	assert.Equal(t, `"dark"`, marshalSettingJSON("dark"))
	assert.Equal(t, `0`, marshalSettingJSON(0))
	assert.Equal(t, `null`, marshalSettingJSON(nil))
	// A channel cannot be marshalled; the fallback must still be a JSON
	// document rather than the empty string.
	assert.Equal(t, `"<undecodable>"`, marshalSettingJSON(make(chan int)))
}

// TestSettingFieldKindRoundTripsBothWays pins the one-table rule: the two
// directions are derived from the SAME pairs, so a kind added to the Go
// schema cannot reach the wire under one name and come back under another.
// The two hand-written switches had already drifted -- one printed
// "boolean" where the Go schema, and the golden account schema that pins
// it, say "bool".
func TestSettingFieldKindRoundTripsBothWays(t *testing.T) {
	kinds := []settings.FieldKind{
		settings.FieldBool, settings.FieldInt, settings.FieldFloat,
		settings.FieldString, settings.FieldEnum, settings.FieldStringList,
		settings.FieldBytes, settings.FieldCustom,
	}
	seen := map[leapmuxv1.SettingFieldKind]bool{}
	for _, kind := range kinds {
		wire := settingFieldKindToProto(kind)
		require.NotEqualf(t, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED, wire,
			"kind %s has no wire value", kind)
		require.Falsef(t, seen[wire], "two kinds map onto the wire value %s", wire)
		seen[wire] = true

		back, ok := SettingFieldKindFromProto(wire)
		require.Truef(t, ok, "wire value %s does not resolve back", wire)
		assert.Equal(t, kind, back)
		assert.Equal(t, kind.String(), back.String())
	}

	// UNSPECIFIED, and a value this build does not know, both report false
	// rather than resolving to FieldBool (the zero kind).
	_, ok := SettingFieldKindFromProto(leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED)
	assert.False(t, ok)
	_, ok = SettingFieldKindFromProto(leapmuxv1.SettingFieldKind(999))
	assert.False(t, ok)
}

// TestEveryDeclaredFieldKindHasAWireValue keeps the table complete: a kind
// added to the schema without a pair here would reach the client as
// UNSPECIFIED, which renders no control at all.
func TestEveryDeclaredFieldKindHasAWireValue(t *testing.T) {
	m := settingsregistry.NewManager(nil, nil)
	descriptors := append(m.Registered(), usersettings.Default.Descriptors()...)
	for _, d := range descriptors {
		for _, f := range d.UI().Fields {
			assert.NotEqualf(t, leapmuxv1.SettingFieldKind_SETTING_FIELD_KIND_UNSPECIFIED,
				settingFieldKindToProto(f.Kind),
				"%s field %q declares kind %s, which has no wire value", d.Name(), f.Name, f.Kind)
		}
	}
}
