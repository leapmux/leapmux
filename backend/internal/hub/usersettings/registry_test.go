package usersettings

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// decoded resolves every registered key through the one read path
// (States), so these degradation tests assert what a caller actually
// sees.
func decoded(prefsJSON string) map[string]any {
	states := Default.States(prefsJSON)
	out := make(map[string]any, len(states))
	for name, state := range states {
		out[name] = state.Value
	}
	return out
}

func TestDecodeEmptyBlobReturnsDefaults(t *testing.T) {
	values := decoded("")
	for _, d := range Default.Descriptors() {
		assert.Equal(t, d.Default(), values[d.Name()], "key %q", d.Name())
	}
	// Spot-check a few defaults.
	assert.Equal(t, "system", values["theme"])
	assert.Equal(t, int64(100), values["turn_end_sound_volume"])
	assert.Equal(t, FontFamilyValue{}, values["ui_fonts"])
}

func TestDecodeBadFieldDegradesOnlyThatField(t *testing.T) {
	blob := `{"theme":"dark","turn_end_sound_volume":900,"diff_view":"unified"}`
	values := decoded(blob)
	assert.Equal(t, "dark", values["theme"], "the healthy key decodes")
	assert.Equal(t, "unified", values["diff_view"], "the healthy key decodes")
	assert.Equal(t, int64(100), values["turn_end_sound_volume"],
		"the out-of-range key degrades to its default, not the whole document")
}

func TestDecodeInvalidStoredValueDegradesToDefault(t *testing.T) {
	// turn_end_sound carries no validator beyond the enum UI hint, so use
	// a key with a real validator: ui_fonts rejects unsanitizable names.
	blob := `{"theme":"dark","ui_fonts":{"enabled":true,"fonts":["  "]}}`
	values := decoded(blob)
	assert.Equal(t, "dark", values["theme"], "the sibling key is untouched")
	assert.Equal(t, FontFamilyValue{}, values["ui_fonts"], "the invalid key degrades to its default")
}

func TestDecodeUndecodableSubDocumentDegradesToDefault(t *testing.T) {
	blob := `{"theme":"dark","debug_logging":{}}`
	values := decoded(blob)
	assert.Equal(t, "dark", values["theme"])
	assert.Equal(t, false, values["debug_logging"], "a wrong-typed sub-document degrades, not the whole blob")
}

func TestDecodeNonObjectBlobUsesDefaults(t *testing.T) {
	values := decoded(`[1,2,3]`)
	assert.Equal(t, "system", values["theme"])
}

func TestApplyPartialMergesOneKeyAndLeavesSiblingsByteIdentical(t *testing.T) {
	blob := `{"theme":"dark","turn_end_sound_volume":42,"orphan_future_key":{"keep":"me"}}`
	merged, err := Default.ApplyPartial(blob, "turn_end_sound_volume", json.RawMessage(`7`))
	require.NoError(t, err)

	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(merged), &doc))
	assert.JSONEq(t, `"dark"`, string(doc["theme"]), "the untouched key keeps its value")
	assert.JSONEq(t, `{"keep":"me"}`, string(doc["orphan_future_key"]),
		"an unknown-to-this-binary key survives verbatim (forward compatibility)")
	assert.JSONEq(t, `7`, string(doc["turn_end_sound_volume"]))
}

func TestApplyPartialWithinKeyOmitsUntouchedFields(t *testing.T) {
	blob := `{"ui_fonts":{"enabled":true,"fonts":["Hack NF"]}}`
	merged, err := Default.ApplyPartial(blob, "ui_fonts", json.RawMessage(`{"enabled":false}`))
	require.NoError(t, err)
	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(merged), &doc))
	assert.JSONEq(t, `{"enabled":false,"fonts":["Hack NF"]}`, string(doc["ui_fonts"]),
		"fields the partial document omits keep their stored values")
}

func TestApplyPartialRefusesInvalidValues(t *testing.T) {
	blob := `{"theme":"dark"}`
	_, err := Default.ApplyPartial(blob, "ui_fonts", json.RawMessage(`{"enabled":true,"fonts":["  "]}`))
	require.Error(t, err)

	// An unknown field name is refused too, so a typo cannot merge to a
	// silent no-op.
	_, err = Default.ApplyPartial(blob, "ui_fonts", json.RawMessage(`{"fonts":["Hack"]}`))
	require.NoError(t, err, "a known partial over a defaulted key applies")
	_, err = Default.ApplyPartial(blob, "ui_fonts", json.RawMessage(`{"fontz":["Hack"]}`))
	require.Error(t, err, "an unknown field name is refused")
}

func TestApplyPartialUnknownKeyRefused(t *testing.T) {
	_, err := Default.ApplyPartial(`{}`, "nope", json.RawMessage(`1`))
	require.Error(t, err)
	// The account scope shares settings.InvalidError, so ONE errors.As at
	// the RPC boundary covers both scopes. A mirrored twin here meant a
	// handler that knew only the other one answered with a 500.
	var invalid *settings.InvalidError
	require.ErrorAs(t, err, &invalid)
}

// TestApplyPartialFontCaps pins the font-stack length cap on BOTH font
// keys. Without it these were the one account key an account could grow
// without limit: the per-name rule caps each entry at 128 bytes and says
// nothing about how many entries the list holds, and users.prefs is TEXT
// in MySQL against effectively unlimited in SQLite and Postgres, so an
// uncapped list also accepts a different maximum per dialect.
func TestApplyPartialFontCaps(t *testing.T) {
	for _, key := range []string{"ui_fonts", "mono_fonts"} {
		t.Run(key, func(t *testing.T) {
			names := make([]string, MaxFonts+1)
			for i := range names {
				names[i] = fmt.Sprintf("Face %d", i)
			}
			over, err := json.Marshal(FontFamilyValue{Enabled: true, Fonts: names})
			require.NoError(t, err)
			_, err = Default.ApplyPartial(`{}`, key, over)
			require.ErrorContains(t, err, "too many font names")
			var invalid *settings.InvalidError
			require.ErrorAs(t, err, &invalid, "an over-long list is a bad argument, not a fault")

			atCap, err := json.Marshal(FontFamilyValue{Enabled: true, Fonts: names[:MaxFonts]})
			require.NoError(t, err)
			out, err := Default.ApplyPartial(`{}`, key, atCap)
			require.NoError(t, err, "exactly MaxFonts names is accepted")

			state, ok := Default.State(out, key)
			require.True(t, ok)
			assert.Len(t, state.Value.(FontFamilyValue).Fonts, MaxFonts)
		})
	}
}

func TestApplyPartialKeybindingCaps(t *testing.T) {
	entries := make([]KeybindingOverride, MaxKeybindings+1)
	for i := range entries {
		entries[i] = KeybindingOverride{Key: "ctrl+k", Command: "cmd"}
	}
	blob, err := json.Marshal(entries)
	require.NoError(t, err)
	_, err = Default.ApplyPartial(`{}`, "keybindings", blob)
	require.ErrorContains(t, err, "too many keybinding overrides")

	entries = entries[:MaxKeybindings]
	blob, err = json.Marshal(entries)
	require.NoError(t, err)
	_, err = Default.ApplyPartial(`{}`, "keybindings", blob)
	require.NoError(t, err)
}

func TestApplyPartialKeybindingFieldLimits(t *testing.T) {
	_, err := Default.ApplyPartial(`{}`, "keybindings", json.RawMessage(`[{"key":"ctrl+k","command":""}]`))
	require.ErrorContains(t, err, "command is required")

	tooLong := strings.Repeat("a", MaxKeybindingLength+1)
	_, err = Default.ApplyPartial(`{}`, "keybindings", json.RawMessage(
		fmt.Sprintf(`[{"key":%q,"command":"cmd"}]`, tooLong)))
	require.ErrorContains(t, err, "key too long")
}

func TestApplyPartialThemeRejectsNonSlug(t *testing.T) {
	_, err := Default.ApplyPartial(`{}`, "theme", json.RawMessage(`"Dark Theme"`))
	require.Error(t, err)
}

func TestResetRemovesOnlyTheNamedKey(t *testing.T) {
	blob := `{"theme":"dark","diff_view":"split"}`
	reset, err := Default.Reset(blob, "theme")
	require.NoError(t, err)
	assert.JSONEq(t, `{"diff_view":"split"}`, reset)

	_, err = Default.Reset(`{}`, "nope")
	require.Error(t, err)
}

func TestStatesResolvesValueRawAndCustomizedForEveryKey(t *testing.T) {
	states := Default.States(`{"theme":"dark"}`)
	require.Len(t, states, len(Default.Descriptors()), "every registered key is present")

	stored := states["theme"]
	assert.Equal(t, "dark", stored.Value)
	assert.JSONEq(t, `"dark"`, string(stored.Raw))
	assert.True(t, stored.Customized)

	absent := states["diff_view"]
	assert.Equal(t, "unified", absent.Value, "an absent key resolves to its default")
	assert.Nil(t, absent.Raw, "an absent key has no stored sub-document")
	assert.False(t, absent.Customized)
}

func TestStatesDegradesOneBadKeyAndKeepsTheRest(t *testing.T) {
	states := Default.States(`{"theme":"dark","ui_fonts":{"enabled":true,"fonts":["  "]}}`)
	assert.Equal(t, "dark", states["theme"].Value, "the sibling key is untouched")
	assert.Equal(t, FontFamilyValue{}, states["ui_fonts"].Value, "the invalid key degrades to its default")
	assert.True(t, states["ui_fonts"].Customized, "it is still a stored value, just not a usable one")
}

func TestStatesOnAMalformedBlobReadsAsAllDefaults(t *testing.T) {
	states := Default.States(`not-json`)
	require.Len(t, states, len(Default.Descriptors()))
	assert.Equal(t, "system", states["theme"].Value)
	assert.False(t, states["theme"].Customized)
}

func TestStateReportsTheStoredSubDocument(t *testing.T) {
	state, ok := Default.State(`{"theme":"dark"}`, "theme")
	require.True(t, ok)
	require.True(t, state.Customized)
	assert.JSONEq(t, `"dark"`, string(state.Raw))

	state, ok = Default.State(`{"theme":"dark"}`, "diff_view")
	require.True(t, ok)
	assert.False(t, state.Customized)
	assert.Nil(t, state.Raw)

	state, ok = Default.State(`not-json`, "theme")
	require.True(t, ok)
	assert.False(t, state.Customized, "a malformed blob reads as no stored value")

	_, ok = Default.State(`{"theme":"dark"}`, "no_such_key")
	assert.False(t, ok, "an unregistered key has no state")
}

// The blob error is typed so the RPC surface can answer FailedPrecondition
// without matching on the message text.
func TestMalformedBlobErrorIsTyped(t *testing.T) {
	_, err := Default.ApplyPartial(`not-json`, "theme", []byte(`"dark"`))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMalformedBlob)

	_, err = Default.Reset(`not-json`, "theme")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMalformedBlob)
}

// TestStateResolvesOneKeyLikeStates pins that the single-key read and the
// whole-blob read cannot disagree — the write path answers through the
// first and the listing through the second, so a key must resolve to the
// same value either way.
func TestStateResolvesOneKeyLikeStates(t *testing.T) {
	blob := `{"theme":"dark","turn_end_sound_volume":42,"diff_view":"not-a-view"}`
	all := Default.States(blob)

	for _, d := range Default.Descriptors() {
		got, ok := Default.State(blob, d.Name())
		require.Truef(t, ok, "%s is registered", d.Name())
		assert.Equalf(t, all[d.Name()], got, "%s must resolve the same both ways", d.Name())
	}

	// A stored value the key's own validator refuses degrades to the
	// default through both paths, and stays marked customized so the row
	// can offer a reset.
	invalid, ok := Default.State(blob, "diff_view")
	require.True(t, ok)
	assert.Equal(t, "unified", invalid.Value, "an invalid stored value degrades to the default")
	assert.True(t, invalid.Customized, "it is still a stored value")

	_, ok = Default.State(blob, "no-such-key")
	assert.False(t, ok, "an unregistered key reports itself as unknown")
}

// TestApplyPartialRefusesAPartialThatChangesNothing pins the empty-partial
// rule on the ACCOUNT scope, in the same classification the instance scope
// uses. An omitted proto3 string arrives as "", and reaching ApplyPartial
// with it produced a bare `EOF` from the JSON decoder -- a message that
// reads as an internal fault rather than a missing argument.
func TestApplyPartialRefusesAPartialThatChangesNothing(t *testing.T) {
	for _, partial := range []string{"", "   ", "{}", "null"} {
		out, err := Default.ApplyPartial(`{"theme":"dark"}`, "theme", json.RawMessage(partial))
		require.Errorf(t, err, "partial %q must be refused", partial)
		var invalid *settings.InvalidError
		assert.ErrorAsf(t, err, &invalid, "partial %q must be an InvalidError, the same class the instance scope uses", partial)
		assert.NotContains(t, err.Error(), "EOF", "the refusal must not surface the decoder's own message")
		assert.Empty(t, out, "a refused write must return no blob")
	}

	// A partial that really specifies a value still works.
	out, err := Default.ApplyPartial(`{"theme":"dark"}`, "theme", json.RawMessage(`"light"`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"theme":"light"}`, out)
}

// TestApplyPartialMergesOntoTheDegradedBaseWhenTheStoredValueIsInvalid
// pins the read/write symmetry on the account scope. The read path
// degrades a stored sub-document its own validator refuses to the key's
// default, so the write path must start from that same base -- otherwise
// the account sees the default and cannot change it.
func TestApplyPartialMergesOntoTheDegradedBaseWhenTheStoredValueIsInvalid(t *testing.T) {
	// ui_fonts rejects unsanitizable names, so this blob reads as default.
	blob := `{"ui_fonts":{"enabled":true,"fonts":["  "]}}`
	state, ok := Default.State(blob, "ui_fonts")
	require.True(t, ok)
	assert.Equal(t, FontFamilyValue{}, state.Value, "the read path degrades the invalid value")

	out, err := Default.ApplyPartial(blob, "ui_fonts", json.RawMessage(`{"enabled":true}`))
	require.NoError(t, err, "a key the reader degrades must stay writable")

	written, ok := Default.State(out, "ui_fonts")
	require.True(t, ok)
	assert.Equal(t, FontFamilyValue{Enabled: true}, written.Value,
		"the merge starts from the default the reader saw, not from the refused document")
}

// TestRegistryRefusesASecretBearingKey pins the account scope's structural
// limit. It stores ONE plaintext JSON blob in users.prefs, so a key that
// declared secret fields would have its secret written in the clear AND
// rendered by a frontend that trusts the descriptor's secret flag.
// Refusing at registration is what makes that mistake impossible.
func TestRegistryRefusesASecretBearingKey(t *testing.T) {
	r := &Registry{byName: map[string]settings.Descriptor{}, warned: map[string]bool{}}
	secretKey := settings.NewKey[struct {
		Token string `json:"token"`
	}]("test.secret").SecretFields("token")

	assert.PanicsWithValue(t,
		`usersettings: key "test.secret" declares secret fields, which the account scope cannot store encrypted`,
		func() { registerDescriptor(r, secretKey) })
}
