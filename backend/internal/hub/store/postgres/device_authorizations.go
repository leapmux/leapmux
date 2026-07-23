package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
	"github.com/leapmux/leapmux/internal/util/userid"
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
		LastPolledAt:    d.LastPolledAt.Ptr(),
		IntervalSeconds: int64(d.IntervalSeconds),
		CreatedAt:       d.CreatedAt.Time,
		ExpiresAt:       d.ExpiresAt.Time,
		ConsumedAt:      d.ConsumedAt.Ptr(),
	}
}

// userIDText is the pgtype twin of sqlutil.NullUserID: a zero (never-minted)
// id becomes SQL NULL, and only a minted one becomes a value. Taking the typed
// id rather than a pre-unwrapped string keeps the "is this set?" question on
// IsZero instead of a raw `s == ""` the type exists to remove.
func userIDText(u userid.UserID) pgtype.Text {
	if u.IsZero() {
		return pgtype.Text{}
	}
	return pgtype.Text{String: u.String(), Valid: true}
}

func (s *deviceAuthorizationStore) Create(ctx context.Context, p store.CreateDeviceAuthorizationParams) error {
	return mapErr(s.conn.q.CreateDeviceAuthorization(ctx, gendb.CreateDeviceAuthorizationParams{
		DeviceCode:      p.DeviceCode,
		UserCode:        p.UserCode,
		DeviceName:      p.DeviceName,
		IntervalSeconds: int32(p.IntervalSeconds),
		ExpiresAt:       pgtime.New(p.ExpiresAt),
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
	return s.conn.q.ApproveDeviceAuthorization(ctx, gendb.ApproveDeviceAuthorizationParams{
		UserID:     userIDText(p.UserID),
		DeviceCode: p.DeviceCode,
	})
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
