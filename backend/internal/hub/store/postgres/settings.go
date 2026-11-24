package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
	"github.com/leapmux/leapmux/internal/util/sqltime/pgtime"
)

type settingsStore struct {
	conn *pgConn
}

var _ store.SettingsStore = (*settingsStore)(nil)

func (c *pgConn) fromDBSetting(r gendb.HubSetting) store.SettingRow {
	return sqlutil.OpenSettingRow(c.ks, r.Key, r.Value, r.Secret, r.UpdatedAt.Time)
}

func (s *settingsStore) GetAll(ctx context.Context) ([]store.SettingRow, error) {
	rows, err := s.conn.q.GetAllSettings(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, s.conn.fromDBSetting), nil
}

// See store.ErrLockingReadOutsideTransaction for the rule and its reason.
func (s *settingsStore) GetAllForUpdate(ctx context.Context) ([]store.SettingRow, error) {
	if !s.conn.inTx() {
		return nil, store.ErrLockingReadOutsideTransaction
	}
	rows, err := s.conn.q.GetAllSettingsForUpdate(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, s.conn.fromDBSetting), nil
}

func (s *settingsStore) Get(ctx context.Context, key string) (*store.SettingRow, error) {
	r, err := s.conn.q.GetSetting(ctx, key)
	if err != nil {
		return nil, mapErr(err)
	}
	row := s.conn.fromDBSetting(r)
	return &row, nil
}

func (s *settingsStore) Upsert(ctx context.Context, p store.UpsertSettingParams) error {
	valCt, secCt, err := sqlutil.SealSetting(s.conn.ks, p)
	if err != nil {
		return err
	}
	return mapErr(s.conn.q.UpsertSetting(ctx, gendb.UpsertSettingParams{
		Key:    p.Key,
		Value:  valCt,
		Secret: secCt,
	}))
}

func (s *settingsStore) InsertIfAbsent(ctx context.Context, p store.UpsertSettingParams) (bool, error) {
	valCt, secCt, err := sqlutil.SealSetting(s.conn.ks, p)
	if err != nil {
		return false, err
	}
	n, err := s.conn.q.InsertSettingIfAbsent(ctx, gendb.InsertSettingIfAbsentParams{
		Key:    p.Key,
		Value:  valCt,
		Secret: secCt,
	})
	return n > 0, mapErr(err)
}

func (s *settingsStore) Delete(ctx context.Context, key string) error {
	return mapErr(s.conn.q.DeleteSetting(ctx, key))
}

type altchaSaltsStore struct {
	conn *pgConn
}

var _ store.AltchaSaltsStore = (*altchaSaltsStore)(nil)

func (s *altchaSaltsStore) ConsumeAltchaSalt(ctx context.Context, p store.ConsumeAltchaSaltParams) (int64, error) {
	rows, err := s.conn.q.ConsumeAltchaSalt(ctx, gendb.ConsumeAltchaSaltParams{
		Salt:      p.Salt,
		ExpiresAt: pgtime.New(p.ExpiresAt),
	})
	if err != nil {
		return 0, mapErr(err)
	}
	return rows, nil
}

func (s *altchaSaltsStore) HasAltchaSalt(ctx context.Context, salt string) (bool, error) {
	used, err := s.conn.q.HasAltchaSalt(ctx, salt)
	if err != nil {
		return false, mapErr(err)
	}
	return used, nil
}
