package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type cliAuthorizationCodeStore struct{ conn *mysqlConn }

var _ store.CLIAuthorizationCodeStore = (*cliAuthorizationCodeStore)(nil)

func fromDBCLIAuthorizationCode(c gendb.CliAuthorizationCode) store.CLIAuthorizationCode {
	return store.CLIAuthorizationCode{
		Code:          c.Code,
		UserID:        c.UserID,
		CodeChallenge: c.CodeChallenge,
		DeviceName:    c.DeviceName,
		CreatedAt:     c.CreatedAt.Time,
		ExpiresAt:     c.ExpiresAt.Time,
		ConsumedAt:    c.ConsumedAt.Ptr(),
	}
}

func (s *cliAuthorizationCodeStore) Create(ctx context.Context, p store.CreateCLIAuthorizationCodeParams) error {
	return mapErr(s.conn.q.CreateCLIAuthorizationCode(ctx, gendb.CreateCLIAuthorizationCodeParams{
		Code:          p.Code,
		UserID:        p.UserID.String(),
		CodeChallenge: p.CodeChallenge,
		DeviceName:    p.DeviceName,
		ExpiresAt:     sqltime.NewMySQLTime(p.ExpiresAt),
	}))
}

func (s *cliAuthorizationCodeStore) GetActive(ctx context.Context, code string) (*store.CLIAuthorizationCode, error) {
	row, err := s.conn.q.GetActiveCLIAuthorizationCode(ctx, code)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBCLIAuthorizationCode(row)
	return &out, nil
}

// Consume implements MySQL's two-step "consume" pattern: first try to mark
// the row as consumed (returning rows-affected); if anything was affected,
// re-read the row to return its contents. RETURNING is unavailable on
// MySQL.
func (s *cliAuthorizationCodeStore) Consume(ctx context.Context, code string) (*store.CLIAuthorizationCode, error) {
	rows, err := rowsAffected(s.conn.q.ConsumeCLIAuthorizationCode(ctx, code))
	if err != nil {
		return nil, err
	}
	if rows == 0 {
		return nil, store.ErrNotFound
	}
	row, err := s.conn.q.GetCLIAuthorizationCode(ctx, code)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBCLIAuthorizationCode(row)
	return &out, nil
}
