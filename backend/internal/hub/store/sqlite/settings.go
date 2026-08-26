package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type settingsStore struct {
	conn *sqliteConn
}

var _ store.SettingsStore = (*settingsStore)(nil)

func fromDBSetting(r gendb.HubSetting) store.SettingRow {
	return store.SettingRow{
		Key:       r.Key,
		Value:     ptrconv.NullStringToPtr(r.Value),
		Secret:    r.Secret,
		UpdatedAt: r.UpdatedAt.Time,
	}
}

func (s *settingsStore) GetAll(ctx context.Context) ([]store.SettingRow, error) {
	rows, err := s.conn.q.GetAllSettings(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return store.MapSlice(rows, fromDBSetting), nil
}

// GetAllForUpdate takes the database writer lock with a no-op write
// before it reads, because SQLite has no SELECT FOR UPDATE. The two
// statements are one unit only inside the caller's transaction, which the
// settings write path always holds.
// See the mysql dialect's note on the same method.
func (s *settingsStore) GetAllForUpdate(ctx context.Context) ([]store.SettingRow, error) {
	if !s.conn.inTx() {
		return nil, store.ErrInvalidArgument
	}
	if err := s.conn.q.LockAllSettings(ctx); err != nil {
		return nil, mapErr(err)
	}
	return s.GetAll(ctx)
}

func (s *settingsStore) Get(ctx context.Context, key string) (*store.SettingRow, error) {
	r, err := s.conn.q.GetSetting(ctx, key)
	if err != nil {
		return nil, mapErr(err)
	}
	row := fromDBSetting(r)
	return &row, nil
}

func (s *settingsStore) Upsert(ctx context.Context, p store.UpsertSettingParams) error {
	return mapErr(s.conn.q.UpsertSetting(ctx, gendb.UpsertSettingParams{
		Key:    p.Key,
		Value:  ptrconv.PtrToNullString(p.Value),
		Secret: p.Secret,
	}))
}

func (s *settingsStore) InsertIfAbsent(ctx context.Context, p store.UpsertSettingParams) (bool, error) {
	n, err := s.conn.q.InsertSettingIfAbsent(ctx, gendb.InsertSettingIfAbsentParams{
		Key:    p.Key,
		Value:  ptrconv.PtrToNullString(p.Value),
		Secret: p.Secret,
	})
	return n > 0, mapErr(err)
}

func (s *settingsStore) Delete(ctx context.Context, key string) error {
	return mapErr(s.conn.q.DeleteSetting(ctx, key))
}

type altchaSaltsStore struct {
	conn *sqliteConn
}

var _ store.AltchaSaltsStore = (*altchaSaltsStore)(nil)

func (s *altchaSaltsStore) ConsumeAltchaSalt(ctx context.Context, p store.ConsumeAltchaSaltParams) (int64, error) {
	rows, err := s.conn.q.ConsumeAltchaSalt(ctx, gendb.ConsumeAltchaSaltParams{
		Salt:      p.Salt,
		ExpiresAt: sqltime.NewSQLiteTime(p.ExpiresAt),
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
