package postgres

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type captchaConfigStore struct {
	conn *pgConn
}

var _ store.CaptchaConfigStore = (*captchaConfigStore)(nil)

func fromDBCaptchaConfig(c gendb.CaptchaConfig) store.CaptchaConfig {
	return store.CaptchaConfig{
		Provider:  c.Provider,
		Selected:  c.Selected,
		Enabled:   c.Enabled,
		Secret:    c.Secret,
		Settings:  c.Settings,
		UpdatedAt: c.UpdatedAt.Time,
	}
}

func (s *captchaConfigStore) GetSelected(ctx context.Context) (*store.CaptchaConfig, error) {
	c, err := s.conn.q.GetSelectedCaptchaConfig(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	cfg := fromDBCaptchaConfig(c)
	return &cfg, nil
}

func (s *captchaConfigStore) Get(ctx context.Context, provider leapmuxv1.CaptchaProvider) (*store.CaptchaConfig, error) {
	c, err := s.conn.q.GetCaptchaConfig(ctx, provider)
	if err != nil {
		return nil, mapErr(err)
	}
	cfg := fromDBCaptchaConfig(c)
	return &cfg, nil
}

func (s *captchaConfigStore) List(ctx context.Context) ([]store.CaptchaConfig, error) {
	rows, err := s.conn.q.ListCaptchaProviders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBCaptchaConfig), nil
}

func (s *captchaConfigStore) InsertIfAbsent(ctx context.Context, p store.InsertCaptchaConfigIfAbsentParams) error {
	return mapErr(s.conn.q.InsertCaptchaConfigIfAbsent(ctx, gendb.InsertCaptchaConfigIfAbsentParams{
		Provider: p.Provider,
		Secret:   p.Secret,
		Settings: p.Settings,
	}))
}

// UpdateSettings rewrites one existing row's settings; the secret is
// never touched.
func (s *captchaConfigStore) UpdateSettings(ctx context.Context, provider leapmuxv1.CaptchaProvider, settings string) error {
	return mapErr(s.conn.q.UpdateCaptchaSettings(ctx, gendb.UpdateCaptchaSettingsParams{Settings: settings, Provider: provider}))
}

func (s *captchaConfigStore) Upsert(ctx context.Context, p store.UpsertCaptchaConfigParams) error {
	return mapErr(s.conn.q.UpsertCaptchaConfig(ctx, gendb.UpsertCaptchaConfigParams{
		Provider: p.Provider,
		Secret:   p.Secret,
		Settings: p.Settings,
	}))
}

// Activate selects and enables the given provider and deselects every
// other row in one statement, so the exactly-one-selected invariant holds
// under concurrent activations: last writer wins, never two selected.
func (s *captchaConfigStore) Activate(ctx context.Context, provider leapmuxv1.CaptchaProvider) error {
	return mapErr(s.conn.q.ActivateCaptchaConfig(ctx, provider))
}

// ActivateIfNoneSelected activates the given provider only when no row is
// selected. The hub's first-use self-heal uses it, so read-path
// provisioning can never override an admin CLI selection that commits
// while a login resolves.
func (s *captchaConfigStore) ActivateIfNoneSelected(ctx context.Context, provider leapmuxv1.CaptchaProvider) error {
	return mapErr(s.conn.q.ActivateCaptchaConfigIfNoneSelected(ctx, provider))
}

func (s *captchaConfigStore) SetEnabled(ctx context.Context, enabled bool) error {
	return mapErr(s.conn.q.SetCaptchaEnabled(ctx, enabled))
}

func (s *captchaConfigStore) Delete(ctx context.Context) error {
	return mapErr(s.conn.q.DeleteCaptchaConfig(ctx))
}

func (s *captchaConfigStore) DeleteProvider(ctx context.Context, provider leapmuxv1.CaptchaProvider) error {
	return mapErr(s.conn.q.DeleteCaptchaConfigProvider(ctx, provider))
}

func (s *captchaConfigStore) ConsumeAltchaSalt(ctx context.Context, p store.ConsumeAltchaSaltParams) (int64, error) {
	rows, err := s.conn.q.ConsumeAltchaSalt(ctx, gendb.ConsumeAltchaSaltParams{
		Salt:      p.Salt,
		ExpiresAt: pgtime.New(p.ExpiresAt),
	})
	if err != nil {
		return 0, mapErr(err)
	}
	return rows, nil
}

type rateLimitConfigStore struct {
	conn *pgConn
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
