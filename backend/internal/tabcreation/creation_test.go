package tabcreation

import (
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
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
