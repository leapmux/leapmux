package postgres

import (
	"context"
	"time"

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
		ClientID:        d.ClientID,
		RequestedScopes: d.RequestedScopes,
		GrantedScopes:   d.GrantedScopes,
		ElevateTokenID:  d.ElevateTokenID.String,
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
		ClientID:        p.ClientID,
		RequestedScopes: p.RequestedScopes,
		IntervalSeconds: int32(p.IntervalSeconds),
		ExpiresAt:       pgtime.New(p.ExpiresAt),
		ElevateTokenID:  textNonEmpty(p.ElevateTokenID),
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
	return s.conn.q.ApproveDeviceAuthorization(ctx, gendb.ApproveDeviceAuthorizationParams{
		UserID:        userIDText(p.UserID),
		DeviceCode:    p.DeviceCode,
		GrantedScopes: p.GrantedScopes,
		Now:           pgtime.New(now),
	})
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
	return s.conn.q.ApproveDeviceAuthorizationByUserCode(ctx, gendb.ApproveDeviceAuthorizationByUserCodeParams{
		UserID:        userIDText(p.UserID),
		UserCode:      p.UserCode,
		GrantedScopes: p.GrantedScopes,
		Now:           pgtime.New(now),
	})
}

func (s *deviceAuthorizationStore) DenyByUserCode(ctx context.Context, userCode string) (int64, error) {
	return s.conn.q.DenyDeviceAuthorizationByUserCode(ctx, userCode)
}

func (s *deviceAuthorizationStore) Consume(ctx context.Context, deviceCode string, now time.Time) (int64, error) {
	return s.conn.q.ConsumeDeviceAuthorization(ctx, gendb.ConsumeDeviceAuthorizationParams{
		DeviceCode: deviceCode,
		Now:        pgtime.New(now),
	})
}

func (s *deviceAuthorizationStore) TouchPoll(ctx context.Context, deviceCode string, now time.Time) error {
	return mapErr(s.conn.q.TouchDeviceAuthorizationPoll(ctx, gendb.TouchDeviceAuthorizationPollParams{
		DeviceCode: deviceCode,
		Now:        pgtime.NewNull(&now),
	}))
}

func (s *deviceAuthorizationStore) ConsumeApprovedForUserClient(ctx context.Context, clientID string, user userid.UserID, now time.Time) (int64, error) {
	if user.IsZero() {
		return 0, store.ErrInvalidArgument
	}
	return rowsAffected(s.conn.q.ConsumeApprovedDeviceAuthorizationsForUserClient(ctx, gendb.ConsumeApprovedDeviceAuthorizationsForUserClientParams{
		ClientID: clientID,
		UserID:   textNonEmpty(user.String()),
		Now:      pgtime.New(now),
	}))
}
