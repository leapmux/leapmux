package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/userid"
)

type deviceAuthorizationStore struct{ conn *sqliteConn }

var _ store.DeviceAuthorizationStore = (*deviceAuthorizationStore)(nil)

func fromDBDeviceAuthorization(d gendb.DeviceAuthorization) store.DeviceAuthorization {
	return store.DeviceAuthorization{
		DeviceCode:      d.DeviceCode,
		UserCode:        d.UserCode,
		DeviceName:      d.DeviceName,
		UserID:          d.UserID.String,
		Approved:        d.Approved,
		LastPolledAt:    d.LastPolledAt.Ptr(),
		IntervalSeconds: d.IntervalSeconds,
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
		IntervalSeconds: p.IntervalSeconds,
		ExpiresAt:       sqltime.NewSQLiteTime(p.ExpiresAt),
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
		Now:           sqltime.NewSQLiteTime(now),
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
		Now:           sqltime.NewSQLiteTime(now),
	}))
}

func (s *deviceAuthorizationStore) DenyByUserCode(ctx context.Context, userCode string) (int64, error) {
	return rowsAffected(s.conn.q.DenyDeviceAuthorizationByUserCode(ctx, userCode))
}

func (s *deviceAuthorizationStore) Consume(ctx context.Context, deviceCode string, now time.Time) (int64, error) {
	return rowsAffected(s.conn.q.ConsumeDeviceAuthorization(ctx, gendb.ConsumeDeviceAuthorizationParams{
		DeviceCode: deviceCode,
		Now:        sqltime.NewSQLiteTime(now),
	}))
}

func (s *deviceAuthorizationStore) TouchPoll(ctx context.Context, deviceCode string, now time.Time) error {
	return mapErr(s.conn.q.TouchDeviceAuthorizationPoll(ctx, gendb.TouchDeviceAuthorizationPollParams{
		DeviceCode: deviceCode,
		Now:        sqltime.NewSQLiteNullTime(&now),
	}))
}

func (s *deviceAuthorizationStore) ConsumeApprovedForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error) {
	if user.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ConsumeApprovedDeviceAuthorizationsForUserClient(ctx, gendb.ConsumeApprovedDeviceAuthorizationsForUserClientParams{
		ClientID: clientID,
		UserID:   sqlutil.NullNonEmpty(user.String()),
		Now:      sqltime.NewSQLiteTime(now),
	}))
}
