package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type deviceAuthorizationStore struct{ conn *pgConn }

var _ store.DeviceAuthorizationStore = (*deviceAuthorizationStore)(nil)

func fromDBDeviceAuthorization(d gendb.DeviceAuthorization) store.DeviceAuthorization {
	uid := ""
	if d.UserID.Valid {
		uid = d.UserID.String
	}
	return store.DeviceAuthorization{
		DeviceCode:      d.DeviceCode,
		UserCode:        d.UserCode,
		DeviceName:      d.DeviceName,
		UserID:          uid,
		Approved:        int64(d.Approved),
		LastPolledAt:    tsToTimePtr(d.LastPolledAt),
		IntervalSeconds: int64(d.IntervalSeconds),
		CreatedAt:       tsToTime(d.CreatedAt),
		ExpiresAt:       tsToTime(d.ExpiresAt),
		ConsumedAt:      tsToTimePtr(d.ConsumedAt),
	}
}

func userIDText(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}

func (s *deviceAuthorizationStore) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	return mapErr(s.conn.q.CreateDeviceAuthorization(ctx, gendb.CreateDeviceAuthorizationParams{
		DeviceCode:      p.DeviceCode,
		UserCode:        p.UserCode,
		DeviceName:      p.DeviceName,
		IntervalSeconds: int32(p.IntervalSeconds),
		ExpiresAt:       timeToTs(p.ExpiresAt),
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
	return s.conn.q.ApproveDeviceAuthorization(ctx, gendb.ApproveDeviceAuthorizationParams{
		UserID:     userIDText(p.UserID),
		DeviceCode: p.DeviceCode,
	})
}

func (s *deviceAuthorizationStore) ApproveByUserCode(ctx context.Context, p store.ApproveDeviceAuthorizationByUserCodeParams) (int64, error) {
	return s.conn.q.ApproveDeviceAuthorizationByUserCode(ctx, gendb.ApproveDeviceAuthorizationByUserCodeParams{
		UserID:   userIDText(p.UserID),
		UserCode: p.UserCode,
	})
}

func (s *deviceAuthorizationStore) Deny(ctx context.Context, deviceCode string) (int64, error) {
	return s.conn.q.DenyDeviceAuthorization(ctx, deviceCode)
}

func (s *deviceAuthorizationStore) Consume(ctx context.Context, deviceCode string) (int64, error) {
	return s.conn.q.ConsumeDeviceAuthorization(ctx, deviceCode)
}

func (s *deviceAuthorizationStore) TouchPoll(ctx context.Context, deviceCode string) error {
	return mapErr(s.conn.q.TouchDeviceAuthorizationPoll(ctx, deviceCode))
}
