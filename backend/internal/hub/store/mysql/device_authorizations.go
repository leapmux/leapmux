package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type deviceAuthorizationStore struct{ conn *mysqlConn }

var _ store.DeviceAuthorizationStore = (*deviceAuthorizationStore)(nil)

func fromDBDeviceAuthorization(d gendb.DeviceAuthorization) store.DeviceAuthorization {
	return store.DeviceAuthorization{
		DeviceCode:      d.DeviceCode,
		UserCode:        d.UserCode,
		DeviceName:      d.DeviceName,
		UserID:          d.UserID.String,
		Approved:        int64(d.Approved),
		LastPolledAt:    d.LastPolledAt.Ptr(),
		IntervalSeconds: int64(d.IntervalSeconds),
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
		IntervalSeconds: int32(p.IntervalSeconds),
		ExpiresAt:       sqltime.NewMySQLTime(p.ExpiresAt),
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

func (s *deviceAuthorizationStore) Approve(ctx context.Context, p store.ApproveDeviceAuthorizationParams) (int64, error) {
	// An approval names WHO approved. A zero id would be written as SQL NULL
	// while the UPDATE still matched the row, so the store would report one
	// row affected, the browser would say "device authorized", and the CLI
	// would then poll authorization_pending forever against a row whose
	// user_id is blank -- told the opposite of what happened. NULL is the
	// legitimate state of a PENDING row, never of an approved one.
	if p.UserID.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ApproveDeviceAuthorization(ctx, gendb.ApproveDeviceAuthorizationParams{
		UserID:     sqlutil.NullUserID(p.UserID),
		DeviceCode: p.DeviceCode,
	}))
}

func (s *deviceAuthorizationStore) ApproveByUserCode(ctx context.Context, p store.ApproveDeviceAuthorizationByUserCodeParams) (int64, error) {
	// An approval names WHO approved. A zero id would be written as SQL NULL
	// while the UPDATE still matched the row, so the store would report one
	// row affected, the browser would say "device authorized", and the CLI
	// would then poll authorization_pending forever against a row whose
	// user_id is blank -- told the opposite of what happened. NULL is the
	// legitimate state of a PENDING row, never of an approved one.
	if p.UserID.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ApproveDeviceAuthorizationByUserCode(ctx, gendb.ApproveDeviceAuthorizationByUserCodeParams{
		UserID:   sqlutil.NullUserID(p.UserID),
		UserCode: p.UserCode,
	}))
}

func (s *deviceAuthorizationStore) Deny(ctx context.Context, deviceCode string) (int64, error) {
	return rowsAffected(s.conn.q.DenyDeviceAuthorization(ctx, deviceCode))
}

func (s *deviceAuthorizationStore) Consume(ctx context.Context, deviceCode string) (int64, error) {
	return rowsAffected(s.conn.q.ConsumeDeviceAuthorization(ctx, deviceCode))
}

func (s *deviceAuthorizationStore) TouchPoll(ctx context.Context, deviceCode string) error {
	return mapErr(s.conn.q.TouchDeviceAuthorizationPoll(ctx, deviceCode))
}
