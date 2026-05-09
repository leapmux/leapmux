package mysql

import (
	"context"
	"errors"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type cliAuthorizationCodeStore struct{ conn *mysqlConn }

var _ store.CLIAuthorizationCodeStore = (*cliAuthorizationCodeStore)(nil)

func fromDBCLIAuthorizationCode(c gendb.CliAuthorizationCode) store.CLIAuthorizationCode {
	return store.CLIAuthorizationCode{
		Code:          c.Code,
		UserID:        c.UserID,
		CodeChallenge: c.CodeChallenge,
		DeviceName:    c.DeviceName,
		CreatedAt:     c.CreatedAt,
		ExpiresAt:     c.ExpiresAt,
		ConsumedAt:    sqlutil.NullTimePtr(c.ConsumedAt),
	}
}

func (s *cliAuthorizationCodeStore) Create(ctx context.Context, p store.CreateCLIAuthorizationCodeParams) error {
	return mapErr(s.conn.q.CreateCLIAuthorizationCode(ctx, gendb.CreateCLIAuthorizationCodeParams{
		Code:          p.Code,
		UserID:        p.UserID,
		CodeChallenge: p.CodeChallenge,
		DeviceName:    p.DeviceName,
		ExpiresAt:     p.ExpiresAt.UTC(),
	}))
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
		return nil, errors.New("cli authorization code not found or expired")
	}
	row, err := s.conn.q.GetCLIAuthorizationCode(ctx, code)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBCLIAuthorizationCode(row)
	return &out, nil
}

func (s *cliAuthorizationCodeStore) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	return rowsAffected(s.conn.q.DeleteExpiredCLIAuthorizationCodes(ctx, cutoff.UTC()))
}
