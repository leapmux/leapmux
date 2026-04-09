package sqlite

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/sqlite/generated/db"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

type orgStore struct {
	q *gendb.Queries
}

var _ store.OrgStore = (*orgStore)(nil)

func fromDBOrg(o gendb.Org) store.Org {
	return store.Org{
		ID:         o.ID,
		Name:       o.Name,
		IsPersonal: ptrconv.Int64ToBool(o.IsPersonal),
		CreatedAt:  o.CreatedAt,
		DeletedAt:  ptrconv.NullTimeToPtr(o.DeletedAt),
	}
}

func fromDBOrgs(rows []gendb.Org) []store.Org {
	return store.MapSlice(rows, fromDBOrg)
}

func (s *orgStore) Create(ctx context.Context, p store.CreateOrgParams) error {
	return mapErr(s.q.CreateOrg(ctx, gendb.CreateOrgParams{
		ID:         p.ID,
		Name:       p.Name,
		IsPersonal: ptrconv.BoolToInt64(p.IsPersonal),
	}))
}

func (s *orgStore) GetByID(ctx context.Context, id string) (*store.Org, error) {
	o, err := s.q.GetOrgByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOrg(o)
	return &out, nil
}

func (s *orgStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Org, error) {
	o, err := s.q.GetOrgByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOrg(o)
	return &out, nil
}

func (s *orgStore) GetByName(ctx context.Context, name string) (*store.Org, error) {
	o, err := s.q.GetOrgByName(ctx, name)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOrg(o)
	return &out, nil
}

func (s *orgStore) HasAny(ctx context.Context) (bool, error) {
	n, err := s.q.HasAnyOrg(ctx)
	if err != nil {
		return false, mapErr(err)
	}
	return ptrconv.Int64ToBool(n), nil
}

func (s *orgStore) ListAll(ctx context.Context, p store.ListAllOrgsParams) ([]store.Org, error) {
	cursor, err := parseCursorToSQLiteTime(p.Cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.ListAllOrgs(ctx, gendb.ListAllOrgsParams{
		Cursor: cursor,
		Limit:  p.Limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOrgs(rows), nil
}

func (s *orgStore) Search(ctx context.Context, p store.SearchOrgsParams) ([]store.Org, error) {
	cursor, err := parseCursorToSQLiteTime(p.Cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.q.SearchOrgs(ctx, gendb.SearchOrgsParams{
		Query:  ptrconv.PtrToNullString(p.Query),
		Cursor: cursor,
		Limit:  p.Limit,
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOrgs(rows), nil
}

func (s *orgStore) UpdateName(ctx context.Context, p store.UpdateOrgNameParams) error {
	return mapErr(s.q.UpdateOrgName(ctx, gendb.UpdateOrgNameParams{
		Name: p.Name,
		ID:   p.ID,
	}))
}

func (s *orgStore) SoftDelete(ctx context.Context, id string) error {
	return mapErr(s.q.SoftDeleteOrg(ctx, id))
}

func (s *orgStore) SoftDeleteNonPersonal(ctx context.Context, id string) error {
	return mapErr(s.q.SoftDeleteNonPersonalOrg(ctx, id))
}
