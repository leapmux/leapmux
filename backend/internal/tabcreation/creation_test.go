package tabcreation

import (
	"encoding/hex"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/emptypb"
)

func validCreation() *leapmuxv1.TabCreation {
	return &leapmuxv1.TabCreation{TabId: "tab", Placement: &leapmuxv1.TabPlacement{WorkspaceId: "workspace", Destination: &leapmuxv1.TabPlacement_TileId{TileId: "tile"}, Insertion: leapmuxv1.TabInsertion_TAB_INSERTION_LAST}}
}
func TestValidate(t *testing.T) {
	for _, kind := range []leapmuxv1.TabType{leapmuxv1.TabType_TAB_TYPE_AGENT, leapmuxv1.TabType_TAB_TYPE_TERMINAL, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE} {
		require.NoError(t, Validate(validCreation(), kind))
	}
	for _, kind := range []leapmuxv1.TabType{0, -1, 999} {
		require.Error(t, Validate(validCreation(), kind))
	}
	require.Error(t, Validate(nil, leapmuxv1.TabType_TAB_TYPE_FILE))
	for _, test := range []struct {
		name   string
		change func(*leapmuxv1.TabCreation)
	}{
		{"empty id", func(c *leapmuxv1.TabCreation) { c.TabId = "" }},
		{"trailing space", func(c *leapmuxv1.TabCreation) { c.TabId = "tab " }},
		{"leading space", func(c *leapmuxv1.TabCreation) { c.TabId = " tab" }},
		{"tab character", func(c *leapmuxv1.TabCreation) { c.TabId = "tab\t" }},
		{"non-generated character", func(c *leapmuxv1.TabCreation) { c.TabId = "tab/one" }},
		{"large id", func(c *leapmuxv1.TabCreation) { c.TabId = strings.Repeat("x", MaxTabIDBytes+1) }},
		{"invalid UTF-8", func(c *leapmuxv1.TabCreation) { c.TabId = string([]byte{255}) }},
		{"missing placement", func(c *leapmuxv1.TabCreation) { c.Placement = nil }},
		{"missing workspace", func(c *leapmuxv1.TabCreation) { c.Placement.WorkspaceId = "" }},
		{"missing destination", func(c *leapmuxv1.TabCreation) { c.Placement.Destination = nil }},
		{"empty tile", func(c *leapmuxv1.TabCreation) { c.Placement.Destination = &leapmuxv1.TabPlacement_TileId{} }},
		{"missing first-leaf marker", func(c *leapmuxv1.TabCreation) { c.Placement.Destination = &leapmuxv1.TabPlacement_FirstLeaf{} }},
		{"unspecified insertion", func(c *leapmuxv1.TabCreation) { c.Placement.Insertion = 0 }},
		{"negative insertion", func(c *leapmuxv1.TabCreation) { c.Placement.Insertion = -1 }},
		{"unknown insertion", func(c *leapmuxv1.TabCreation) { c.Placement.Insertion = 999 }},
		{"missing reference", func(c *leapmuxv1.TabCreation) { c.Placement.Insertion = leapmuxv1.TabInsertion_TAB_INSERTION_AFTER }},
		{"unexpected reference", func(c *leapmuxv1.TabCreation) { c.Placement.RelativeTabId = "other" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := validCreation()
			test.change(c)
			require.Error(t, Validate(c, leapmuxv1.TabType_TAB_TYPE_FILE))
		})
	}
	boundary := validCreation()
	boundary.TabId = strings.Repeat("x", MaxTabIDBytes)
	require.NoError(t, Validate(boundary, leapmuxv1.TabType_TAB_TYPE_FILE))
	boundary.Placement.Destination = &leapmuxv1.TabPlacement_FirstLeaf{FirstLeaf: &emptypb.Empty{}}
	require.NoError(t, Validate(boundary, leapmuxv1.TabType_TAB_TYPE_FILE))
	for _, insertion := range []leapmuxv1.TabInsertion{leapmuxv1.TabInsertion_TAB_INSERTION_BEFORE, leapmuxv1.TabInsertion_TAB_INSERTION_AFTER} {
		boundary.Placement.Insertion = insertion
		boundary.Placement.RelativeTabId = "reference"
		require.NoError(t, Validate(boundary, leapmuxv1.TabType_TAB_TYPE_FILE))
	}
}
func TestFingerprint(t *testing.T) {
	original := &leapmuxv1.OpenAgentRequest{Options: map[string]string{"model": "one", "effort": "high"}, Creation: validCreation()}
	reordered := &leapmuxv1.OpenAgentRequest{Options: map[string]string{"effort": "high", "model": "one"}, Creation: proto.CloneOf(original.Creation)}
	first, err := Fingerprint(original)
	require.NoError(t, err)
	second, err := Fingerprint(reordered)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	reordered.Creation.TabId = "different"
	second, err = Fingerprint(reordered)
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	_, err = Fingerprint(nil)
	require.Error(t, err)
	_, err = Fingerprint(&leapmuxv1.TabCreation{TabId: string([]byte{255})})
	require.Error(t, err)
}

func TestTabPresentationProtocol(t *testing.T) {
	t.Parallel()
	fields := (&leapmuxv1.TabPresentation{}).ProtoReflect().Descriptor().Fields()
	require.Equal(t, 3, fields.Len())
	for i, expected := range []struct {
		field, enum string
		members     []string
	}{
		{"display_mode", "TabDisplayMode", []string{"UNSPECIFIED", "RENDER", "SOURCE", "SPLIT"}},
		{"file_view_mode", "TabFileViewMode", []string{"UNSPECIFIED", "WORKING", "HEAD", "STAGED", "UNIFIED_DIFF", "SPLIT_DIFF"}},
	} {
		field := fields.ByName(protoreflect.Name(expected.field))
		require.NotNil(t, field)
		assert.Equal(t, protoreflect.FieldNumber(i+1), field.Number())
		assert.True(t, field.HasPresence())
		require.Equal(t, protoreflect.EnumKind, field.Kind())
		enum := field.Enum()
		assert.Equal(t, expected.enum, string(enum.Name()))
		require.Equal(t, len(expected.members), enum.Values().Len())
		prefix := "TAB_DISPLAY_MODE_"
		if i == 1 {
			prefix = "TAB_FILE_VIEW_MODE_"
		}
		for ordinal, member := range expected.members {
			value := enum.Values().Get(ordinal)
			assert.Equal(t, protoreflect.EnumNumber(ordinal), value.Number())
			assert.Equal(t, prefix+member, string(value.Name()))
		}
	}
	diffBase := fields.ByName("file_diff_base")
	require.NotNil(t, diffBase)
	assert.Equal(t, protoreflect.FieldNumber(3), diffBase.Number())
	assert.Equal(t, protoreflect.StringKind, diffBase.Kind())
	assert.True(t, diffBase.HasPresence())
}

func TestTabPresentationNumericEncodingAndPresence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		value *leapmuxv1.TabPresentation
		wire  string
	}{
		{"absent", &leapmuxv1.TabPresentation{}, ""},
		{"explicit zero", &leapmuxv1.TabPresentation{DisplayMode: leapmuxv1.TabDisplayMode(0).Enum(), FileViewMode: leapmuxv1.TabFileViewMode(0).Enum(), FileDiffBase: proto.String("")}, "080010001a00"},
		{"known", &leapmuxv1.TabPresentation{DisplayMode: leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_RENDER.Enum(), FileViewMode: leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNIFIED_DIFF.Enum()}, "08011004"},
		{"unknown positive", &leapmuxv1.TabPresentation{DisplayMode: leapmuxv1.TabDisplayMode(32768).Enum(), FileViewMode: leapmuxv1.TabFileViewMode(32768).Enum()}, "0880800210808002"},
		{"unknown negative", &leapmuxv1.TabPresentation{DisplayMode: leapmuxv1.TabDisplayMode(-1).Enum(), FileViewMode: leapmuxv1.TabFileViewMode(-1).Enum()}, "08ffffffffffffffffff0110ffffffffffffffffff01"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := proto.Marshal(test.value)
			require.NoError(t, err)
			assert.Equal(t, test.wire, hex.EncodeToString(encoded))
			var decoded leapmuxv1.TabPresentation
			require.NoError(t, proto.Unmarshal(encoded, &decoded))
			assert.True(t, proto.Equal(test.value, &decoded))
			creation := validCreation()
			creation.Presentation = &decoded
			require.NoError(t, Validate(creation, leapmuxv1.TabType_TAB_TYPE_FILE))
		})
	}
}
