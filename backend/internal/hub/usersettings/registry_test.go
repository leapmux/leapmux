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
	assert.Equal(t, ThemeValue{Name: "default", Mode: "system"}, values["theme"])
	assert.Equal(t, int64(100), values["turn_end_sound_volume"])
	assert.Equal(t, FontFamilyValue{}, values["ui_fonts"])
}

func TestDecodeBadFieldDegradesOnlyThatField(t *testing.T) {
	blob := `{"diff_view":"split","turn_end_sound_volume":900,"turn_end_sound":"none"}`
	values := decoded(blob)
	assert.Equal(t, "split", values["diff_view"], "the healthy key decodes")
	assert.Equal(t, "none", values["turn_end_sound"], "the healthy key decodes")
	assert.Equal(t, int64(100), values["turn_end_sound_volume"],
		"the out-of-range key degrades to its default, not the whole document")
}

func TestDecodeInvalidStoredValueDegradesToDefault(t *testing.T) {
	// turn_end_sound carries no validator beyond the enum UI hint, so use
	// a key with a real validator: ui_fonts rejects unsanitizable names.
	blob := `{"diff_view":"split","ui_fonts":{"enabled":true,"fonts":["  "]}}`
	values := decoded(blob)
	assert.Equal(t, "split", values["diff_view"], "the sibling key is untouched")
	assert.Equal(t, FontFamilyValue{}, values["ui_fonts"], "the invalid key degrades to its default")
}

func TestDecodeUndecodableSubDocumentDegradesToDefault(t *testing.T) {
	blob := `{"diff_view":"split","debug_logging":{}}`
	values := decoded(blob)
	assert.Equal(t, "split", values["diff_view"])
	assert.Equal(t, false, values["debug_logging"], "a wrong-typed sub-document degrades, not the whole blob")
}

func TestDecodeNonObjectBlobUsesDefaults(t *testing.T) {
	values := decoded(`[1,2,3]`)
	assert.Equal(t, ThemeValue{Name: "default", Mode: "system"}, values["theme"])
}

func TestApplyPartialMergesOneKeyAndLeavesSiblingsByteIdentical(t *testing.T) {
	blob := `{"terminal_theme":"dark","turn_end_sound_volume":42,"orphan_future_key":{"keep":"me"}}`
	merged, err := Default.ApplyPartial(blob, "turn_end_sound_volume", json.RawMessage(`7`))
	require.NoError(t, err)

	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(merged), &doc))
	assert.JSONEq(t, `"dark"`, string(doc["terminal_theme"]), "the untouched key keeps its value")
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
	blob := `{"terminal_theme":"dark"}`
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

func TestApplyPartialThemeRejectsANonSlugName(t *testing.T) {
	_, err := Default.ApplyPartial(`{}`, "theme", json.RawMessage(`{"name":"Dark Theme","mode":"dark"}`))
	require.ErrorContains(t, err, "invalid theme name")

	_, err = Default.ApplyPartial(`{}`, "theme", json.RawMessage(`{"name":"rose_pine","mode":"dark"}`))
	require.ErrorContains(t, err, "invalid theme name")
}

func TestApplyPartialThemeRejectsAnUnlistedMode(t *testing.T) {
	// The mode IS a closed set this package owns, unlike the palette name.
	_, err := Default.ApplyPartial(`{}`, "theme", json.RawMessage(`{"name":"default","mode":"sepia"}`))
	require.ErrorContains(t, err, "mode:")
}

func TestApplyPartialThemeAcceptsANameThisBinaryDoesNotKnow(t *testing.T) {
	// The palette catalogue lives in the client, so a NEWER client must be
	// able to store a theme this hub has never heard of. Refusing here would
	// make the hub the authority on a list it cannot see.
	out, err := Default.ApplyPartial(`{}`, "theme", json.RawMessage(`{"name":"some-future-theme","mode":"dark"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"theme":{"name":"some-future-theme","mode":"dark"}}`, out)
}

func TestApplyPartialThemeKeepsTheSiblingFieldItDoesNotName(t *testing.T) {
	// The two halves are one key but still merge per field, so switching the
	// mode from the tri-switch must not reset the palette to the default.
	blob := `{"theme":{"name":"catppuccin","mode":"light"}}`
	out, err := Default.ApplyPartial(blob, "theme", json.RawMessage(`{"mode":"dark"}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"theme":{"name":"catppuccin","mode":"dark"}}`, out)
}

func TestThemeDefaultMatchesTheClientCatalogue(t *testing.T) {
	// DefaultThemeName must equal DEFAULT_THEME_ID in
	// frontend/src/styles/themes/index.ts. The client falls back to that id for
	// a name it cannot resolve, so a disagreement here would let the hub store
	// a default the client silently replaces with a different palette.
	assert.Equal(t, "default", DefaultThemeName)
	assert.Equal(t, ThemeValue{Name: DefaultThemeName, Mode: "system"}, KeyTheme.Default())
}

func TestResetRemovesOnlyTheNamedKey(t *testing.T) {
	blob := `{"theme":{"name":"nord","mode":"dark"},"diff_view":"split"}`
	reset, err := Default.Reset(blob, "theme")
	require.NoError(t, err)
	assert.JSONEq(t, `{"diff_view":"split"}`, reset)

	_, err = Default.Reset(`{}`, "nope")
	require.Error(t, err)
}

func TestStatesResolvesValueRawAndCustomizedForEveryKey(t *testing.T) {
	states := Default.States(`{"theme":{"name":"nord","mode":"dark"}}`)
	require.Len(t, states, len(Default.Descriptors()), "every registered key is present")

	stored := states["theme"]
	assert.Equal(t, ThemeValue{Name: "nord", Mode: "dark"}, stored.Value)
	assert.JSONEq(t, `{"name":"nord","mode":"dark"}`, string(stored.Raw))
	assert.True(t, stored.Customized)

	absent := states["diff_view"]
	assert.Equal(t, "unified", absent.Value, "an absent key resolves to its default")
	assert.Nil(t, absent.Raw, "an absent key has no stored sub-document")
	assert.False(t, absent.Customized)
}

func TestStatesDegradesOneBadKeyAndKeepsTheRest(t *testing.T) {
	states := Default.States(`{"diff_view":"split","ui_fonts":{"enabled":true,"fonts":["  "]}}`)
	assert.Equal(t, "split", states["diff_view"].Value, "the sibling key is untouched")
	assert.Equal(t, FontFamilyValue{}, states["ui_fonts"].Value, "the invalid key degrades to its default")
	assert.True(t, states["ui_fonts"].Customized, "it is still a stored value, just not a usable one")
}

func TestStatesOnAMalformedBlobReadsAsAllDefaults(t *testing.T) {
	states := Default.States(`not-json`)
	require.Len(t, states, len(Default.Descriptors()))
	assert.Equal(t, ThemeValue{Name: "default", Mode: "system"}, states["theme"].Value)
	assert.False(t, states["theme"].Customized)
}

func TestStateReportsTheStoredSubDocument(t *testing.T) {
	state, ok := Default.State(`{"theme":{"name":"nord","mode":"dark"}}`, "theme")
	require.True(t, ok)
	require.True(t, state.Customized)
	assert.JSONEq(t, `{"name":"nord","mode":"dark"}`, string(state.Raw))

	state, ok = Default.State(`{"theme":{"name":"nord","mode":"dark"}}`, "diff_view")
	require.True(t, ok)
	assert.False(t, state.Customized)
	assert.Nil(t, state.Raw)

	state, ok = Default.State(`not-json`, "theme")
	require.True(t, ok)
	assert.False(t, state.Customized, "a malformed blob reads as no stored value")

	_, ok = Default.State(`{"theme":{"name":"nord","mode":"dark"}}`, "no_such_key")
	assert.False(t, ok, "an unregistered key has no state")
}

// The blob error is typed so the RPC surface can answer FailedPrecondition
// without matching on the message text.
func TestMalformedBlobErrorIsTyped(t *testing.T) {
	_, err := Default.ApplyPartial(`not-json`, "theme", []byte(`{"mode":"dark"}`))
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
	blob := `{"theme":{"name":"nord","mode":"dark"},"turn_end_sound_volume":42,"diff_view":"not-a-view"}`
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
		out, err := Default.ApplyPartial(`{"theme":{"mode":"dark"}}`, "theme", json.RawMessage(partial))
		require.Errorf(t, err, "partial %q must be refused", partial)
		var invalid *settings.InvalidError
		assert.ErrorAsf(t, err, &invalid, "partial %q must be an InvalidError, the same class the instance scope uses", partial)
		assert.NotContains(t, err.Error(), "EOF", "the refusal must not surface the decoder's own message")
		assert.Empty(t, out, "a refused write must return no blob")
	}

	// A partial that really specifies a value still works.
	out, err := Default.ApplyPartial(`{"theme":{"mode":"dark"}}`, "theme", json.RawMessage(`{"mode":"light"}`))
	require.NoError(t, err)
	// `name` comes back as the key's default, not as "": the merge base is the
	// DECODED stored value, which starts from Default() and has the stored
	// sub-document unmarshalled over it. A field the stored document omits
	// therefore keeps the default rather than the zero value.
	assert.JSONEq(t, `{"theme":{"name":"default","mode":"light"}}`, out)
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

// Returning a row to "Match UI" must not be refused by the variant it used to
// carry.
//
// `ApplyPartial` merges onto the STORED value and leaves an omitted field
// untouched. The chooser clears the variant by sending `variant: undefined`,
// which JSON.stringify OMITS -- so the partial says nothing about the variant
// and the merge keeps the stored one. A validator that refuses a following row
// carrying a variant then fires on merge residue that no client ever sent, and
// the row can never go back to the sentinel for the life of the account.
func TestFollowingRowDropsTheVariantTheMergeCarriedOver(t *testing.T) {
	for _, key := range []string{"terminal_theme", "syntax_theme"} {
		t.Run(key, func(t *testing.T) {
			blob := `{"` + key + `":{"name":"catppuccin","mode":"dark","variant":{"dark":"catppuccin-macchiato"}}}`

			merged, err := Default.ApplyPartial(blob, key, json.RawMessage(`{"name":"match-ui","mode":"match-ui"}`))
			require.NoError(t, err, "returning to the sentinel must not be refused")

			doc := map[string]json.RawMessage{}
			require.NoError(t, json.Unmarshal([]byte(merged), &doc))
			assert.JSONEq(t, `{"name":"match-ui","mode":"match-ui"}`, string(doc[key]),
				"a row that follows the app carries no variant of its own")
		})
	}
}

// A variant names one look of ONE palette, so it stops meaning anything the
// moment the name beside it changes. The chooser states this by clearing the
// variant with every palette pick, and the merge would otherwise keep it --
// storing a Catppuccin variant under a Nord theme.
func TestChangingThePaletteDropsTheVariantOfThePreviousOne(t *testing.T) {
	blob := `{"theme":{"name":"catppuccin","mode":"dark","variant":{"dark":"catppuccin-macchiato"}}}`

	merged, err := Default.ApplyPartial(blob, "theme", json.RawMessage(`{"name":"nord"}`))
	require.NoError(t, err)

	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(merged), &doc))
	assert.JSONEq(t, `{"name":"nord","mode":"dark"}`, string(doc["theme"]),
		"the previous palette's variant does not survive a palette change")
}

// The clear must not over-reach: a partial that leaves the NAME alone keeps the
// variant, which is what a bare mode change sends.
func TestAnUntouchedNameKeepsItsVariant(t *testing.T) {
	blob := `{"theme":{"name":"catppuccin","mode":"dark","variant":{"dark":"catppuccin-macchiato"}}}`

	merged, err := Default.ApplyPartial(blob, "theme", json.RawMessage(`{"mode":"light"}`))
	require.NoError(t, err)

	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(merged), &doc))
	assert.JSONEq(t, `{"name":"catppuccin","mode":"light","variant":{"dark":"catppuccin-macchiato"}}`,
		string(doc["theme"]), "a mode change keeps the variant it did not mention")
}

// The write that used to be a data-loss risk is now simply ACCEPTED.
//
// MaxKeybindings entries x three MaxKeybindingLength fields is about 160 KB --
// every entry individually legal, and 2.4x the 65,535 bytes MySQL's TEXT column
// held. On a relaxed sql_mode MySQL truncated that mid-JSON, after which the
// next read failed and every account setting reset to its default. The column
// is MEDIUMTEXT now, so the storage no longer imposes a ceiling the product
// never chose.
func TestApplyPartialAcceptsTheLargestLegalKeybindingList(t *testing.T) {
	long := strings.Repeat("k", MaxKeybindingLength)
	entries := make([]map[string]string, 0, MaxKeybindings)
	for range MaxKeybindings {
		entries = append(entries, map[string]string{"key": long, "command": long, "when": long})
	}
	payload, err := json.Marshal(entries)
	require.NoError(t, err)
	require.Greater(t, len(payload), 65535, "the fixture must exceed what MySQL's TEXT column held")

	merged, err := Default.ApplyPartial(`{}`, "keybindings", json.RawMessage(payload))
	require.NoError(t, err, "every entry is within its declared cap, so the write is legal")
	assert.LessOrEqual(t, len(merged), MaxPrefsBytes)
}

// The document cap still bounds what no per-key cap can see.
//
// An unknown key is the one uncapped contributor: decodeBlob preserves a key
// this binary does not know verbatim, for forward compatibility, so a blob
// written by a later build carries residue no validator here bounds. Refusing
// the write is the right answer -- the alternative is a document the storage
// truncates, which loses every setting rather than one write.
func TestApplyPartialRefusesAnOversizedDocument(t *testing.T) {
	residue, err := json.Marshal(strings.Repeat("x", MaxPrefsBytes))
	require.NoError(t, err)
	blob := `{"orphan_future_key":` + string(residue) + `}`

	_, err = Default.ApplyPartial(blob, "theme", json.RawMessage(`{"name":"nord","mode":"dark"}`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "too large")
}

// The cap must not refuse an ordinary document. A realistic account sits far
// under it, so no normal write is affected.
func TestApplyPartialAcceptsAnOrdinaryDocument(t *testing.T) {
	merged, err := Default.ApplyPartial(`{}`, "theme", json.RawMessage(`{"name":"nord","mode":"dark"}`))
	require.NoError(t, err)
	assert.Less(t, len(merged), MaxPrefsBytes)
}

// TestApplyPartialTrayKeysMergeIndependently is what proves the five-scalar-
// keys decision at the storage layer.
//
// The Desktop settings could have been one object-shaped key, which would have
// rendered the same five rows. This is the difference: each key merges alone,
// so a device override of one of them cannot drag the other four onto the
// device tier with it.
func TestApplyPartialTrayKeysMergeIndependently(t *testing.T) {
	blob := `{"tray_enabled":true,"tray_on_close":"quit","tray_on_minimize":"tray","start_on_login":true,"start_minimized":"minimized"}`
	merged, err := Default.ApplyPartial(blob, "tray_on_close", json.RawMessage(`"tray"`))
	require.NoError(t, err)

	doc := map[string]json.RawMessage{}
	require.NoError(t, json.Unmarshal([]byte(merged), &doc))
	assert.JSONEq(t, `"tray"`, string(doc["tray_on_close"]))
	assert.JSONEq(t, `true`, string(doc["tray_enabled"]), "the sibling keeps its value")
	assert.JSONEq(t, `"tray"`, string(doc["tray_on_minimize"]))
	assert.JSONEq(t, `true`, string(doc["start_on_login"]))
	assert.JSONEq(t, `"minimized"`, string(doc["start_minimized"]))
}

// A token the contract does not declare must store NOTHING. The Rust shell
// refuses it too, so a stored value here would be a preference the user set
// and no client obeys.
func TestApplyPartialRefusesAnUnlistedDesktopToken(t *testing.T) {
	blob := `{"tray_on_close":"tray"}`
	for _, tc := range []struct{ key, value string }{
		{"tray_on_close", `"minimize"`},
		{"tray_on_minimize", `"quit"`},
		{"start_minimized", `"hidden"`},
	} {
		// The RETURNED document is what the caller stores, so it is what the
		// test must read. Re-parsing `blob` would assert only that a Go string
		// is immutable, and would stay green if ApplyPartial wrote the rejected
		// value before validating it.
		out, err := Default.ApplyPartial(blob, tc.key, json.RawMessage(tc.value))
		require.Errorf(t, err, "%s must refuse %s", tc.key, tc.value)
		assert.Emptyf(t, out, "%s must return no document to store", tc.key)
	}
}
