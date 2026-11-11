package crdt

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/tabcreation"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCaptureTabPublication(t *testing.T) {
	t.Parallel()
	newRequest := func() *leapmuxv1.TabPublication {
		return &leapmuxv1.TabPublication{
			TabType: leapmuxv1.TabType_TAB_TYPE_FILE,
			Creation: &leapmuxv1.TabCreation{
				TabId: "capture",
				Placement: &leapmuxv1.TabPlacement{
					WorkspaceId: "workspace",
					Destination: &leapmuxv1.TabPlacement_TileId{TileId: "tile"},
					Insertion:   leapmuxv1.TabInsertion_TAB_INSERTION_LAST,
				},
				Presentation: &leapmuxv1.TabPresentation{DisplayMode: leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_SOURCE.Enum()},
			},
		}
	}
	t.Run("invalid descriptor", func(t *testing.T) {
		for _, request := range []*leapmuxv1.TabPublication{nil, {}, {TabType: leapmuxv1.TabType_TAB_TYPE_FILE}} {
			captured, hash, err := captureTabPublication(request)
			require.Error(t, err)
			assert.Nil(t, captured)
			assert.Nil(t, hash)
		}
	})
	t.Run("fingerprint encoding failure", func(t *testing.T) {
		request := newRequest()
		request.Creation.Presentation.FileDiffBase = proto.String(string([]byte{255}))
		require.NoError(t, tabcreation.Validate(request.Creation, request.TabType))
		captured, hash, err := captureTabPublication(request)
		require.ErrorContains(t, err, "encode creation request")
		assert.Nil(t, captured)
		assert.Nil(t, hash)
	})
	t.Run("immutable descriptor", func(t *testing.T) {
		request := newRequest()
		expected := proto.CloneOf(request)
		expectedHash, err := tabcreation.Fingerprint(request)
		require.NoError(t, err)
		captured, hash, err := captureTabPublication(request)
		require.NoError(t, err)
		assert.NotSame(t, request, captured)
		assert.NotSame(t, request.Creation, captured.Creation)
		assert.NotSame(t, request.Creation.Placement, captured.Creation.Placement)
		assert.NotSame(t, request.Creation.Presentation, captured.Creation.Presentation)
		request.TabType = leapmuxv1.TabType_TAB_TYPE_AGENT
		request.Creation.TabId = "changed"
		request.Creation.Placement.WorkspaceId = "other"
		request.Creation.Presentation.DisplayMode = leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_RENDER.Enum()
		assert.True(t, proto.Equal(expected, captured))
		assert.Equal(t, expectedHash, hash)
	})
}
