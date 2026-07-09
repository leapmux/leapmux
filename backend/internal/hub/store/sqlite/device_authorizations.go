package sqlite

import (
	"context"
	"database/sql"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
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
		LastPolledAt:    sqlutil.NullTimePtr(d.LastPolledAt),
		IntervalSeconds: d.IntervalSeconds,
		CreatedAt:       d.CreatedAt,
		ExpiresAt:       d.ExpiresAt,
		ConsumedAt:      sqlutil.NullTimePtr(d.ConsumedAt),
	}
}

func (s *deviceAuthorizationStore) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	return mapErr(s.conn.q.CreateDeviceAuthorization(ctx, gendb.CreateDeviceAuthorizationParams{
		DeviceCode:      p.DeviceCode,
		UserCode:        p.UserCode,
		DeviceName:      p.DeviceName,
		IntervalSeconds: p.IntervalSeconds,
		ExpiresAt:       p.ExpiresAt.UTC(),
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
	return rowsAffected(s.conn.q.ApproveDeviceAuthorization(ctx, gendb.ApproveDeviceAuthorizationParams{
		UserID:     sql.NullString{String: p.UserID, Valid: p.UserID != ""},
		DeviceCode: p.DeviceCode,
	}))
}

func (s *deviceAuthorizationStore) ApproveByUserCode(ctx context.Context, p store.ApproveDeviceAuthorizationByUserCodeParams) (int64, error) {
	return rowsAffected(s.conn.q.ApproveDeviceAuthorizationByUserCode(ctx, gendb.ApproveDeviceAuthorizationByUserCodeParams{
		UserID:   sql.NullString{String: p.UserID, Valid: p.UserID != ""},
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
