package store

import (
	"bytes"
	"fmt"

	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// NewUpsertUserStateParams derives the tenant, payload, and retention watermark
// from one state. The caller must not mutate state during construction.
// Later changes cannot alter the returned snapshot.
func NewUpsertUserStateParams(state *leapmuxv1.UserCrdtState) (UpsertUserStateParams, error) {
	if state == nil {
		return UpsertUserStateParams{}, fmt.Errorf("%w: user state is required", ErrInvalidArgument)
	}
	owner, ok := userid.New(state.GetUserId())
	if !ok {
		return UpsertUserStateParams{}, fmt.Errorf("%w: user state requires a user id", ErrInvalidArgument)
	}
	payload, err := proto.Marshal(state)
	if err != nil {
		return UpsertUserStateParams{}, fmt.Errorf("marshal user state: %w", err)
	}
	return UpsertUserStateParams{
		userID: owner, statePayload: payload,
		compactionPhysicalMs: state.GetCompactionWatermark().GetPhysical(),
	}, nil
}

// UserID identifies the owner of the serialized state.
func (p UpsertUserStateParams) UserID() userid.UserID { return p.userID }

// StatePayload returns a copy so a reader cannot change the retention snapshot.
func (p UpsertUserStateParams) StatePayload() []byte { return bytes.Clone(p.statePayload) }

// CompactionPhysicalMs is the serialized state's own compaction physical time.
// The cross-user retention sweep uses it to keep unabsorbed journal records.
func (p UpsertUserStateParams) CompactionPhysicalMs() int64 { return p.compactionPhysicalMs }
