package sqlite

import (
	"context"
	"time"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type cliAuthorizationCodeStore struct{ conn *sqliteConn }

var _ store.CLIAuthorizationCodeStore = (*cliAuthorizationCodeStore)(nil)

func fromDBCLIAuthorizationCode(c gendb.CliAuthorizationCode) store.CLIAuthorizationCode {
	return store.CLIAuthorizationCode{
		Code:          c.Code,
		UserID:        c.UserID,
		CodeChallenge: c.CodeChallenge,
		DeviceName:    c.DeviceName,
		AdminScope:    c.AdminScope,
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
		AdminScope:    p.AdminScope,
		ExpiresAt:     sqltime.NewSQLiteTime(p.ExpiresAt),
	}))
}

func (s *cliAuthorizationCodeStore) GetActive(ctx context.Context, code string, now time.Time) (*store.CLIAuthorizationCode, error) {
	row, err := s.conn.q.GetActiveCLIAuthorizationCode(ctx, gendb.GetActiveCLIAuthorizationCodeParams{
		Code: code,
		Now:  sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBCLIAuthorizationCode(row)
	return &out, nil
}

func (s *cliAuthorizationCodeStore) Consume(ctx context.Context, code string, now time.Time) (*store.CLIAuthorizationCode, error) {
	row, err := s.conn.q.ConsumeCLIAuthorizationCode(ctx, gendb.ConsumeCLIAuthorizationCodeParams{
		Code: code,
		Now:  sqltime.NewSQLiteTime(now),
	})
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBCLIAuthorizationCode(row)
	return &out, nil
}
