package store_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestUpsertUserStateParamsKeepOneImmutableSnapshot(t *testing.T) {
	state := &leapmuxv1.UserCrdtState{
		UserId: "owner", CurrentEpoch: 1,
		CompactionWatermark: &leapmuxv1.HLC{Physical: 120, Logical: 7, ClientId: "canonical"},
	}
	params, err := store.NewUpsertUserStateParams(state)
	require.NoError(t, err)
	state.UserId, state.CurrentEpoch, state.CompactionWatermark.Physical = "changed", 8, 999
	payload := params.StatePayload()
	require.NotEmpty(t, payload)
	payload[0] ^= 0xff
	decoded := &leapmuxv1.UserCrdtState{}
	require.NoError(t, proto.Unmarshal(params.StatePayload(), decoded))
	assert.True(t, params.UserID().Matches("owner"))
	assert.Equal(t, "owner", decoded.GetUserId())
	assert.EqualValues(t, 1, decoded.GetCurrentEpoch())
	assert.EqualValues(t, 120, decoded.GetCompactionWatermark().GetPhysical())
	assert.EqualValues(t, 7, decoded.GetCompactionWatermark().GetLogical())
	assert.EqualValues(t, 120, params.CompactionPhysicalMs())
	typ := reflect.TypeFor[store.UpsertUserStateParams]()
	for i := range typ.NumField() {
		assert.False(t, typ.Field(i).IsExported(), "the snapshot fields must not be independently writable")
	}
}

func TestUpsertUserStateParamsDeriveEveryPhysicalValueFromThePayload(t *testing.T) {
	for _, watermark := range []*leapmuxv1.HLC{
		nil, {}, {Physical: -1}, {Physical: 1}, {Physical: math.MaxInt64},
	} {
		params, err := store.NewUpsertUserStateParams(&leapmuxv1.UserCrdtState{
			UserId: "owner", CurrentEpoch: 1, CompactionWatermark: watermark,
		})
		require.NoError(t, err)
		decoded := &leapmuxv1.UserCrdtState{}
		require.NoError(t, proto.Unmarshal(params.StatePayload(), decoded))
		assert.Equal(t, watermark.GetPhysical(), params.CompactionPhysicalMs())
		assert.Equal(t, decoded.GetCompactionWatermark().GetPhysical(), params.CompactionPhysicalMs())
	}
}

func TestUpsertUserStateParamsRefuseAbsentIdentityAndMarshalFailures(t *testing.T) {
	for _, state := range []*leapmuxv1.UserCrdtState{nil, {}} {
		_, err := store.NewUpsertUserStateParams(state)
		assert.ErrorIs(t, err, store.ErrInvalidArgument)
	}
	_, err := store.NewUpsertUserStateParams(&leapmuxv1.UserCrdtState{UserId: "\xff"})
	assert.ErrorContains(t, err, "marshal user state")
}
