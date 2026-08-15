package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type captchaConfigStore struct {
	conn *mysqlConn
}

var _ store.CaptchaConfigStore = (*captchaConfigStore)(nil)

func fromDBCaptchaConfig(c gendb.CaptchaConfig) *store.CaptchaConfig {
	return &store.CaptchaConfig{
		Enabled:                c.Enabled,
		Algorithm:              c.Algorithm,
		Cost:                   c.Cost,
		MemoryCost:             c.MemoryCost,
		Parallelism:            c.Parallelism,
		ChallengeExpirySeconds: c.ChallengeExpirySeconds,
		Secret:                 c.Secret,
		UpdatedAt:              c.UpdatedAt.Time,
	}
}

func (s *captchaConfigStore) Get(ctx context.Context) (*store.CaptchaConfig, error) {
	c, err := s.conn.q.GetCaptchaConfig(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBCaptchaConfig(c), nil
}

func (s *captchaConfigStore) Insert(ctx context.Context, p store.InsertCaptchaConfigParams) error {
	return mapErr(s.conn.q.InsertCaptchaConfig(ctx, gendb.InsertCaptchaConfigParams{
		Enabled:                p.Enabled,
		Algorithm:              p.Algorithm,
		Cost:                   p.Cost,
		MemoryCost:             p.MemoryCost,
		Parallelism:            p.Parallelism,
		ChallengeExpirySeconds: p.ChallengeExpirySeconds,
		Secret:                 p.Secret,
	}))
}

func (s *captchaConfigStore) Update(ctx context.Context, p store.UpdateCaptchaConfigParams) error {
	return mapErr(s.conn.q.UpdateCaptchaConfig(ctx, gendb.UpdateCaptchaConfigParams{
		Enabled:                p.Enabled,
		Algorithm:              p.Algorithm,
		Cost:                   p.Cost,
		MemoryCost:             p.MemoryCost,
		Parallelism:            p.Parallelism,
		ChallengeExpirySeconds: p.ChallengeExpirySeconds,
	}))
}

func (s *captchaConfigStore) Delete(ctx context.Context) error {
	return mapErr(s.conn.q.DeleteCaptchaConfig(ctx))
}

func (s *captchaConfigStore) ConsumeCaptchaSalt(ctx context.Context, p store.ConsumeCaptchaSaltParams) (int64, error) {
	rows, err := s.conn.q.ConsumeCaptchaSalt(ctx, gendb.ConsumeCaptchaSaltParams{
		Salt:      p.Salt,
		ExpiresAt: sqltime.NewMySQLTime(p.ExpiresAt),
	})
	if err != nil {
		return 0, mapErr(err)
	}
	return rows, nil
}

type rateLimitConfigStore struct {
	conn *mysqlConn
}

var _ store.RateLimitConfigStore = (*rateLimitConfigStore)(nil)

func fromDBRateLimitConfig(r gendb.RateLimitConfig) store.RateLimitConfig {
	return store.RateLimitConfig{
		Operation:     r.Operation,
		Enabled:       r.Enabled,
		MaxAttempts:   r.MaxAttempts,
		WindowSeconds: r.WindowSeconds,
		UpdatedAt:     r.UpdatedAt.Time,
	}
}

func (s *rateLimitConfigStore) Get(ctx context.Context, operation string) (*store.RateLimitConfig, error) {
	r, err := s.conn.q.GetRateLimitConfig(ctx, operation)
	if err != nil {
		return nil, mapErr(err)
	}
	c := fromDBRateLimitConfig(r)
	return &c, nil
}

func (s *rateLimitConfigStore) List(ctx context.Context) ([]store.RateLimitConfig, error) {
	rows, err := s.conn.q.ListRateLimitConfigs(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBRateLimitConfig), nil
}

func (s *rateLimitConfigStore) Upsert(ctx context.Context, p store.UpsertRateLimitConfigParams) error {
	return mapErr(s.conn.q.UpsertRateLimitConfig(ctx, gendb.UpsertRateLimitConfigParams{
		Operation:     p.Operation,
		Enabled:       p.Enabled,
		MaxAttempts:   p.MaxAttempts,
		WindowSeconds: p.WindowSeconds,
	}))
}

func (s *rateLimitConfigStore) Delete(ctx context.Context, operation string) error {
	return mapErr(s.conn.q.DeleteRateLimitConfig(ctx, operation))
}
