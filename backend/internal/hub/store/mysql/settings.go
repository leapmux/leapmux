package mysql

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/mysql/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/sqltime"
)

type settingsStore struct {
	conn *mysqlConn
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

func (s *settingsStore) Get(ctx context.Context, key string) (*store.SettingRow, error) {
	r, err := s.conn.q.GetSetting(ctx, key)
	if err != nil {
		return nil, mapErr(err)
	}
	row := fromDBSetting(r)
	return &row, nil
}

func (s *settingsStore) GetForUpdate(ctx context.Context, key string) (*store.SettingRow, error) {
	r, err := s.conn.q.GetSettingForUpdate(ctx, key)
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

// InsertIfAbsent treats a duplicate key as "not inserted" (false, nil),
// not a fault: a racing provisioner won. See the query comment for why
// the duplicate arrives as error 1062 rather than an affected-rows count.
func (s *settingsStore) InsertIfAbsent(ctx context.Context, p store.UpsertSettingParams) (bool, error) {
	n, err := s.conn.q.InsertSettingIfAbsent(ctx, gendb.InsertSettingIfAbsentParams{
		Key:    p.Key,
		Value:  ptrconv.PtrToNullString(p.Value),
		Secret: p.Secret,
	})
	if err != nil {
		if isDupEntry(err) {
			return false, nil
		}
		return false, mapErr(err)
	}
	return n > 0, nil
}

func (s *settingsStore) Delete(ctx context.Context, key string) error {
	return mapErr(s.conn.q.DeleteSetting(ctx, key))
}

type altchaSaltsStore struct {
	conn *mysqlConn
}

var _ store.AltchaSaltsStore = (*altchaSaltsStore)(nil)

// ConsumeAltchaSalt treats a duplicate salt as a replay (0 rows), not a
// fault: 1 row = first use accepted. The duplicate arrives as MySQL
// error 1062 rather than an affected-rows count, because the connection
// runs with clientFoundRows, under which ON DUPLICATE KEY UPDATE would
// report the duplicate as 1.
func (s *altchaSaltsStore) ConsumeAltchaSalt(ctx context.Context, p store.ConsumeAltchaSaltParams) (int64, error) {
	rows, err := s.conn.q.ConsumeAltchaSalt(ctx, gendb.ConsumeAltchaSaltParams{
		Salt:      p.Salt,
		ExpiresAt: sqltime.NewMySQLTime(p.ExpiresAt),
	})
	if err != nil {
		if isDupEntry(err) {
			return 0, nil
		}
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
