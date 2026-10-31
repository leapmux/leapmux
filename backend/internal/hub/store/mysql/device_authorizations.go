package mysql

import (
	"context"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type deviceAuthorizationStore struct{ conn *mysqlConn }

var _ store.DeviceAuthorizationStore = (*deviceAuthorizationStore)(nil)

func fromDBDeviceAuthorization(d gendb.DeviceAuthorization) store.DeviceAuthorization {
	return store.DeviceAuthorization{
		DeviceCode:      d.DeviceCode,
		UserCode:        d.UserCode,
		DeviceName:      d.DeviceName,
		UserID:          d.UserID.String,
		State:           d.State,
		LastPolledAt:    d.LastPolledAt.Ptr(),
		ClientID:        d.ClientID,
		RequestedScopes: d.RequestedScopes,
		GrantedScopes:   d.GrantedScopes,
		ElevateTokenID:  d.ElevateTokenID.String,
		CreatedAt:       d.CreatedAt.Time,
		ExpiresAt:       d.ExpiresAt.Time,
		ConsumedAt:      d.ConsumedAt.Ptr(),
	}
}

func (s *deviceAuthorizationStore) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	return mapErr(s.conn.q.CreateDeviceAuthorization(ctx, gendb.CreateDeviceAuthorizationParams{
		DeviceCode:      p.DeviceCode,
		UserCode:        p.UserCode,
		DeviceName:      p.DeviceName,
		ClientID:        p.ClientID,
		RequestedScopes: p.RequestedScopes,
		State:           leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING,
		ExpiresAt:       sqltime.NewMySQLTime(p.ExpiresAt),
		ElevateTokenID:  sqlutil.NullNonEmpty(p.ElevateTokenID),
	}))
}

func (s *deviceAuthorizationStore) Get(ctx context.Context, deviceCode string) (*store.DeviceAuthorization, error) {
	d, err := s.conn.q.GetDeviceAuthorization(ctx, deviceCode)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBDeviceAuthorization(d)
	return &out, nil
}

func (s *deviceAuthorizationStore) GetByUserCode(ctx context.Context, userCode string) (*store.DeviceAuthorization, error) {
	d, err := s.conn.q.GetDeviceAuthorizationByUserCode(ctx, userCode)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBDeviceAuthorization(d)
	return &out, nil
}

func (s *deviceAuthorizationStore) Approve(ctx context.Context, p store.ApproveDeviceAuthorizationParams, now time.Time) (int64, error) {
	// An approval identifies WHO approved. The store would write a zero id as
	// SQL NULL while the UPDATE still matched the row, so it would report one
	// row affected, the browser would say "device authorized", and the CLI
	// would then poll authorization_pending forever against a row whose
	// user_id is blank -- told the opposite of what happened. NULL is the
	// legitimate state of a PENDING row, never of an approved one.
	if p.UserID.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ApproveDeviceAuthorization(ctx, gendb.ApproveDeviceAuthorizationParams{
		UserID:        sqlutil.NullUserID(p.UserID),
		DeviceCode:    p.DeviceCode,
		GrantedScopes: p.GrantedScopes,
		ApprovedState: leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_APPROVED,
		PendingState:  leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING,
		Now:           sqltime.NewMySQLTime(now),
	}))
}

func (s *deviceAuthorizationStore) ApproveByUserCode(ctx context.Context, p store.ApproveDeviceAuthorizationByUserCodeParams, now time.Time) (int64, error) {
	// An approval identifies WHO approved. The store would write a zero id as
	// SQL NULL while the UPDATE still matched the row, so it would report one
	// row affected, the browser would say "device authorized", and the CLI
	// would then poll authorization_pending forever against a row whose
	// user_id is blank -- told the opposite of what happened. NULL is the
	// legitimate state of a PENDING row, never of an approved one.
	if p.UserID.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ApproveDeviceAuthorizationByUserCode(ctx, gendb.ApproveDeviceAuthorizationByUserCodeParams{
		UserID:        sqlutil.NullUserID(p.UserID),
		UserCode:      p.UserCode,
		GrantedScopes: p.GrantedScopes,
		ApprovedState: leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_APPROVED,
		PendingState:  leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING,
		Now:           sqltime.NewMySQLTime(now),
	}))
}

func (s *deviceAuthorizationStore) DenyByUserCode(ctx context.Context, userCode string) (int64, error) {
	return rowsAffected(s.conn.q.DenyDeviceAuthorizationByUserCode(ctx, gendb.DenyDeviceAuthorizationByUserCodeParams{
		UserCode:     userCode,
		DeniedState:  leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_DENIED,
		PendingState: leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_PENDING,
	}))
}

func (s *deviceAuthorizationStore) Consume(ctx context.Context, deviceCode string, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.ConsumeDeviceAuthorization(ctx, gendb.ConsumeDeviceAuthorizationParams{
		DeviceCode:    deviceCode,
		ApprovedState: leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_APPROVED,
		Now:           sqltime.NewMySQLTime(now),
	}))
}

func (s *deviceAuthorizationStore) TouchPoll(ctx context.Context, deviceCode string, now time.Time) error {
	return mapErr(s.conn.q.TouchDeviceAuthorizationPoll(ctx, gendb.TouchDeviceAuthorizationPollParams{
		DeviceCode: deviceCode,
		Now:        sqltime.NewMySQLNullTime(&now),
	}))
}

func (s *deviceAuthorizationStore) ConsumeApprovedForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error) {
	if user.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ConsumeApprovedDeviceAuthorizationsForUserClient(ctx, gendb.ConsumeApprovedDeviceAuthorizationsForUserClientParams{
		ClientID:      clientID,
		ApprovedState: leapmuxv1.DeviceAuthorizationState_DEVICE_AUTHORIZATION_STATE_APPROVED,
		UserID:        sqlutil.NullNonEmpty(user.String()),
		Now:           sqltime.NewMySQLTime(now),
	}))
}
