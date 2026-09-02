package usersettings

import (
	"encoding/json"
	"flag"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata/account_schema.json from the declared keys")

// goldenPath is the ONE artifact both scopes are pinned to.
//
// The account settings are declared here AND in the frontend's
// browser registry, which owns their PRESENTATION because it also renders
// a device-local override the wire cannot carry. This file pins what the
// two must agree on, and nothing else: the key set, the field names and
// kinds, the allowed enum VALUES, the numeric bounds, and the two facts
// that decide which control a client builds (the unit and the
// custom-editor id).
//
// Labels are deliberately absent. They are presentation, the registry
// owns them, and pinning them here made the hub carry a second copy of
// text nothing on this side renders — which is how "Side by side" and
// "Side-by-Side" came to disagree. What a client MUST NOT get wrong is
// offering a value the hub refuses, and that is what the values and the
// bounds pin. The frontend half is
// frontend/src/components/settings/registry/index.test.ts.
//
// "And nothing else" is a decision, not a licence to forget: every
// declared member of settings.UIMeta, settings.Field, and
// settings.EnumValue must be recorded below OR carry a stated reason in
// omittedByPolicy or omittedWhileUnused. See
// TestGoldenRecordsEveryDeclaredMember.
const goldenPath = "testdata/account_schema.json"

type goldenOption struct {
	Value string `json:"value"`
}

// goldenField records every declared fact that decides WHICH CONTROL a
// client builds for the field, and nothing that only decides how that
// control is worded.
//
// Unit and CustomID are here because each one changes the control rather
// than its wording. A Unit of "percent" turns an integer field into a
// slider, and a FieldCustom whose CustomID the client does not carry
// renders no row at all (see controlForField in the frontend's
// protoRegistry). While the golden omitted them, the frontend test suite
// had to restate both by hand to build a fixture that rendered the same
// controls production does.
//
// Add a member here to record a newly declared fact. The name must match
// the settings.Field member it records, because that name is how the
// tripwire below decides what is recorded.
type goldenField struct {
	Name       string         `json:"name"`
	Kind       string         `json:"kind"`
	EnumValues []goldenOption `json:"enumValues,omitempty"`
	Min        *int64         `json:"min,omitempty"`
	Max        *int64         `json:"max,omitempty"`
	Unit       string         `json:"unit,omitempty"`
	CustomID   string         `json:"customId,omitempty"`
}

// goldenKey records one declared key. Category, Title, and Fields record
// the settings.UIMeta members of the same name. Key is the one member
// with no UIMeta counterpart: it comes from settings.Descriptor.Name.
type goldenKey struct {
	Key      string        `json:"key"`
	Category string        `json:"category"`
	Title    string        `json:"title"`
	Fields   []goldenField `json:"fields"`
}

// toGoldenOption records one allowed enum value.
func toGoldenOption(ev settings.EnumValue) goldenOption {
	return goldenOption{Value: ev.Value}
}

// toGoldenField records one declared field.
func toGoldenField(f settings.Field) goldenField {
	gf := goldenField{
		Name:     f.Name,
		Kind:     f.Kind.String(),
		Min:      f.Min,
		Max:      f.Max,
		Unit:     f.Unit,
		CustomID: f.CustomID,
	}
	for _, ev := range f.EnumValues {
		gf.EnumValues = append(gf.EnumValues, toGoldenOption(ev))
	}
	return gf
}

// toGoldenKey records one declared key. The name comes from the
// descriptor; everything else comes from its presentation metadata.
func toGoldenKey(name string, ui settings.UIMeta) goldenKey {
	k := goldenKey{Key: name, Category: ui.Category, Title: ui.Title}
	for _, f := range ui.Fields {
		k.Fields = append(k.Fields, toGoldenField(f))
	}
	return k
}

func declaredSchema() []goldenKey {
	descriptors := Default.Descriptors()
	out := make([]goldenKey, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, toGoldenKey(d.Name(), d.UI()))
	}
	return out
}

func TestAccountSchemaMatchesGolden(t *testing.T) {
	want, err := json.MarshalIndent(declaredSchema(), "", "  ")
	require.NoError(t, err)
	want = append(want, '\n')

	if *updateGolden {
		require.NoError(t, os.WriteFile(goldenPath, want, 0o600))
		t.Log("rewrote " + goldenPath)
		return
	}

	got, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "the golden schema is missing; run `go test ./internal/hub/usersettings -update-golden`")
	assert.Equal(t, string(want), string(got),
		"the declared account keys no longer match %s.\n"+
			"The frontend registry binds these keys and reads the same file, so update BOTH:\n"+
			"  1. go test ./internal/hub/usersettings -update-golden\n"+
			"  2. mirror the change in frontend/src/components/settings/registry/settings.ts",
		goldenPath)
}

// goldenMirror pairs one declared struct the golden reads with the struct
// that records it.
//
// The RECORDED member set is DERIVED from the golden struct's own field
// names, never restated. So a member added to goldenField counts as
// recorded with no second edit here, and a member renamed on the declared
// struct stops matching and fails the walk.
type goldenMirror struct {
	name   string
	source reflect.Type
	golden reflect.Type
}

var (
	uiMetaMirror    = goldenMirror{"UIMeta", reflect.TypeFor[settings.UIMeta](), reflect.TypeFor[goldenKey]()}
	fieldMirror     = goldenMirror{"Field", reflect.TypeFor[settings.Field](), reflect.TypeFor[goldenField]()}
	enumValueMirror = goldenMirror{"EnumValue", reflect.TypeFor[settings.EnumValue](), reflect.TypeFor[goldenOption]()}
)

// goldenMirrors lists every declared struct the golden reads. A struct
// the golden starts reading must be added here, or nothing checks it.
var goldenMirrors = []goldenMirror{uiMetaMirror, fieldMirror, enumValueMirror}

// omittedByPolicy lists the declared members the golden refuses to record
// EVEN WHEN an account key sets them, with the reason the hub is not the
// authority for that member. An entry here is a decision. Do not "repair"
// it by recording the member.
var omittedByPolicy = map[string]string{
	"Field.Label": "presentation. The frontend registry declares its own label for every account row, " +
		"and a second copy on this side is what let \"Side by side\" and \"Side-by-Side\" disagree",
	"EnumValue.Label": "presentation, for the reason Field.Label states",
	"Field.Help":      "presentation, for the reason Field.Label states; the registry declares its own help text",
	"EnumValue.Help":  "presentation, for the reason Field.Label states",
	"UIMeta.Summary": "presentation, and no client renders it. Summary is the one-line description " +
		"`leapmux control admin settings` prints; the frontend reads label and help from its own registry",
}

// omittedWhileUnused lists the declared members NO account key sets
// today. The golden drops each one, so the day a key starts setting one,
// the hub and the frontend registry can disagree with the golden file
// unchanged.
//
// TestOmittedMembersStayUnused is what makes that impossible: it fails on
// the first account key that sets one of these. The repair is then to
// record the member in the golden struct and delete the entry here — NOT
// to widen this table.
var omittedWhileUnused = map[string]string{
	"Field.MinF": "no account key declares a float bound. The one numeric account field, " +
		"turn_end_sound_volume, is an integer percentage",
	"Field.MaxF": "no account key declares a float bound, for the reason Field.MinF states",
	"Field.Secret": "the account scope stores ONE plaintext JSON blob in users.prefs, so registerDescriptor " +
		"panics on a key that declares secret fields. A Secret flag here is a defect, not a fact to record",
	"Field.Placeholder": "no account key declares a free-text string field, which is the only kind a " +
		"placeholder hints at. Every account field is an enum, a bool, an integer, a string list, or a custom editor",
	"Field.DependsOn": "no account key declares a visibility condition. The frontend registry's hiddenWhen owns " +
		"the account rows that hide, because each of them reads a " +
		"browser-tier value the wire cannot carry",
	"UIMeta.HiddenInSolo": "solo hiding is instance scope only. A solo hub has one user, whose account settings " +
		"all stay editable",
}

// exportedMembers lists one declared struct's exported members in
// declaration order, paired with their index.
func exportedMembers(typ reflect.Type) []reflect.StructField {
	out := make([]reflect.StructField, 0, typ.NumField())
	for i := range typ.NumField() {
		f := typ.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		out = append(out, f)
	}
	return out
}

// TestGoldenRecordsEveryDeclaredMember is the tripwire that makes a
// silently unrecorded hub fact impossible.
//
// The golden hand-copies a chosen subset of settings.Field into
// goldenField. That subset is the point — the file pins the control, not
// the wording — but a hand-copy has no way to notice a member added to
// the declared struct after it was written. Unit and CustomID both
// arrived that way: they were declared, dropped by the copy, and the
// golden file stayed byte-identical while the frontend restated them by
// hand. A member added tomorrow would repeat exactly that.
//
// So every exported member of every mirrored struct must have ONE home:
// recorded in the golden struct, or listed with a reason in
// omittedByPolicy or omittedWhileUnused. A new member has neither, and
// this walk names it.
func TestGoldenRecordsEveryDeclaredMember(t *testing.T) {
	for _, m := range goldenMirrors {
		for _, member := range exportedMembers(m.source) {
			label := m.name + "." + member.Name
			_, recorded := m.golden.FieldByName(member.Name)
			policyWhy, byPolicy := omittedByPolicy[label]
			unusedWhy, whileUnused := omittedWhileUnused[label]

			if byPolicy {
				assert.NotEmptyf(t, policyWhy, "omittedByPolicy: the entry for %q needs a reason", label)
			}
			if whileUnused {
				assert.NotEmptyf(t, unusedWhy, "omittedWhileUnused: the entry for %q needs a reason", label)
			}
			assert.Truef(t, recorded || byPolicy || whileUnused,
				"settings.%s declares %s, which the golden neither records nor omits on purpose.\n"+
					"Either record it — add a member named %s to the %s struct in this file, set it in the\n"+
					"conversion, and rerun with -update-golden to refresh %s — or add %q to omittedByPolicy\n"+
					"(the hub is not the authority for it) or to omittedWhileUnused (no account key sets it),\n"+
					"with the reason.",
				m.name, member.Name, member.Name, m.golden.Name(), goldenPath, label)
		}
	}
}

// TestGoldenOmissionsAreLive keeps the two omission tables honest. An
// entry whose member no longer exists is a stale claim; an entry whose
// member the golden now records is a contradiction; an entry in BOTH
// tables claims two incompatible reasons at once. Each one would let the
// next real omission hide behind a reason that reads as deliberate.
func TestGoldenOmissionsAreLive(t *testing.T) {
	for label := range omittedByPolicy {
		_, alsoUnused := omittedWhileUnused[label]
		assert.Falsef(t, alsoUnused,
			"%q is in BOTH omission tables: omittedByPolicy says the hub is not the authority for it, "+
				"and omittedWhileUnused says a key that sets it must be recorded. Keep one.", label)
	}

	declared := map[string]bool{}
	recorded := map[string]bool{}
	for _, m := range goldenMirrors {
		for _, member := range exportedMembers(m.source) {
			label := m.name + "." + member.Name
			declared[label] = true
			if _, ok := m.golden.FieldByName(member.Name); ok {
				recorded[label] = true
			}
		}
	}

	for _, table := range []struct {
		name    string
		entries map[string]string
	}{
		{"omittedByPolicy", omittedByPolicy},
		{"omittedWhileUnused", omittedWhileUnused},
	} {
		for label, why := range table.entries {
			assert.NotEmptyf(t, why, "%s: the entry for %q needs a reason", table.name, label)
			if !assert.Truef(t, declared[label],
				"%s omits %q, which no mirrored struct declares; delete the entry", table.name, label) {
				continue
			}
			assert.Falsef(t, recorded[label],
				"%s omits %q, which the golden now records; delete the entry", table.name, label)
		}
	}
}

// TestGoldenCopiesEveryRecordedMember closes the hole the structural walk
// leaves open: it reads a member as recorded because the golden struct
// DECLARES a field of that name, and a declared field that the conversion
// never assigns records nothing.
//
// Each conversion therefore runs on a probe whose every declared member
// is non-zero, and every recorded member must come out non-zero. The
// probe is deliberately not a legal declaration (it carries a CustomID on
// an enum field and a Secret flag the account scope refuses) — the
// conversions copy, they do not validate, and settings.TestSchemaMatchesValueTypes
// is what refuses an illegal declaration.
func TestGoldenCopiesEveryRecordedMember(t *testing.T) {
	probeMin, probeMax := int64(-7), int64(7)
	probeMinF, probeMaxF := -0.5, 0.5
	probeEnumValue := settings.EnumValue{Value: "probe-value", Label: "probe-enum-label", Help: "probe-enum-help"}
	probeField := settings.Field{
		Name:        "probe-name",
		Label:       "probe-label",
		Help:        "probe-help",
		Kind:        settings.FieldEnum,
		EnumValues:  []settings.EnumValue{probeEnumValue},
		Min:         &probeMin,
		Max:         &probeMax,
		MinF:        &probeMinF,
		MaxF:        &probeMaxF,
		Unit:        "probe-unit",
		Secret:      true,
		Placeholder: "probe-placeholder",
		DependsOn:   &settings.FieldCondition{Field: "probe-sibling", In: []string{"probe-value"}},
		CustomID:    "probe-custom",
	}
	probeUI := settings.UIMeta{
		Category:     "probe-category",
		Title:        "probe-title",
		Summary:      "probe-summary",
		HiddenInSolo: true,
		Fields:       []settings.Field{probeField},
	}

	// Every declared member of the probes is non-zero, so nothing below
	// can pass because the source value happened to be empty.
	requireEveryMemberIsSet(t, enumValueMirror, reflect.ValueOf(probeEnumValue))
	requireEveryMemberIsSet(t, fieldMirror, reflect.ValueOf(probeField))
	requireEveryMemberIsSet(t, uiMetaMirror, reflect.ValueOf(probeUI))

	assertRecordedMembersSurvive(t, enumValueMirror, reflect.ValueOf(toGoldenOption(probeEnumValue)))
	assertRecordedMembersSurvive(t, fieldMirror, reflect.ValueOf(toGoldenField(probeField)))
	assertRecordedMembersSurvive(t, uiMetaMirror, reflect.ValueOf(toGoldenKey("probe-key", probeUI)))
}

// requireEveryMemberIsSet fails when a probe leaves a declared member at
// its zero value, because the survival check below would then prove
// nothing about that member.
//
// It covers EVERY declared member, not only the recorded ones, so the
// probe stays complete by construction. A member the golden starts
// recording later is then already set, and the survival check is sound
// the moment it applies.
func requireEveryMemberIsSet(t *testing.T, m goldenMirror, probe reflect.Value) {
	t.Helper()
	for _, member := range exportedMembers(m.source) {
		require.Falsef(t, probe.FieldByIndex(member.Index).IsZero(),
			"the %s probe leaves %s at its zero value; set it, so the probe stays complete "+
				"and the survival walk is sound if the golden ever records %s",
			m.name, member.Name, member.Name)
	}
}

// assertRecordedMembersSurvive checks that each member the golden records
// carries a non-zero value out of the conversion.
func assertRecordedMembersSurvive(t *testing.T, m goldenMirror, got reflect.Value) {
	t.Helper()
	for _, member := range exportedMembers(m.source) {
		recorded, ok := m.golden.FieldByName(member.Name)
		if !ok {
			continue // omitted; the two tables own that case
		}
		assert.Falsef(t, got.FieldByIndex(recorded.Index).IsZero(),
			"%s.%s: the golden declares the member but the conversion leaves it zero "+
				"for a probe that sets every member", m.golden.Name(), member.Name)
	}
}

// TestOmittedMembersStayUnused supplies the half of the tripwire the
// structural walk cannot.
//
// omittedWhileUnused drops a member because no account key sets it. The
// day a key does — a placeholder on a new string field, a DependsOn that
// hides a row, a float bound — the golden drops that fact,
// testdata/account_schema.json stays byte-identical,
// TestAccountSchemaMatchesGolden passes, and the frontend registry is
// free to disagree with the hub. This walks every declared key and fails
// on the first one that sets an omitted member.
//
// omittedByPolicy is deliberately NOT checked here: those members exist
// to be set (every account key declares a Summary) and the golden drops
// them on purpose.
func TestOmittedMembersStayUnused(t *testing.T) {
	descriptors := Default.Descriptors()
	require.NotEmpty(t, descriptors, "no account key was walked; the tripwire proved nothing")
	for _, d := range descriptors {
		ui := d.UI()
		where := "account key " + d.Name()
		assertOmittedMembersAreZero(t, where, uiMetaMirror, reflect.ValueOf(ui))
		for _, f := range ui.Fields {
			fieldWhere := where
			if f.Name != "" {
				fieldWhere += " field " + f.Name
			}
			assertOmittedMembersAreZero(t, fieldWhere, fieldMirror, reflect.ValueOf(f))
			for _, ev := range f.EnumValues {
				assertOmittedMembersAreZero(t, fieldWhere+" enum value "+ev.Value,
					enumValueMirror, reflect.ValueOf(ev))
			}
		}
	}
}

// assertOmittedMembersAreZero fails when a declared value sets a member
// that omittedWhileUnused drops because nothing sets it.
func assertOmittedMembersAreZero(t *testing.T, where string, m goldenMirror, v reflect.Value) {
	t.Helper()
	for _, member := range exportedMembers(m.source) {
		label := m.name + "." + member.Name
		if _, omitted := omittedWhileUnused[label]; !omitted {
			continue
		}
		assert.Truef(t, v.FieldByIndex(member.Index).IsZero(),
			"%s sets %s, which %s does not record.\n"+
				"Record it — add a member named %s to the golden struct, delete the omittedWhileUnused\n"+
				"entry, rerun with -update-golden, and mirror the change in the frontend registry.",
			where, label, goldenPath, member.Name)
	}
}
