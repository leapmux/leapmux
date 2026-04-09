package postgres

import (
	"context"

	"github.com/leapmux/leapmux/internal/hub/store"
	gendb "github.com/leapmux/leapmux/internal/hub/store/postgres/generated/db"
)

type orgStore struct {
	conn *pgConn
}

var _ store.OrgStore = (*orgStore)(nil)

func fromDBOrg(o gendb.Org) store.Org {
	return store.Org{
		ID:         o.ID,
		Name:       o.Name,
		IsPersonal: o.IsPersonal,
		CreatedAt:  tsToTime(o.CreatedAt),
		DeletedAt:  tsToTimePtr(o.DeletedAt),
	}
}

func (s *orgStore) Create(ctx context.Context, p store.CreateOrgParams) error {
	return mapErr(s.conn.q.CreateOrg(ctx, gendb.CreateOrgParams{
		ID:         p.ID,
		Name:       p.Name,
		IsPersonal: p.IsPersonal,
	}))
}

func (s *orgStore) GetByID(ctx context.Context, id string) (*store.Org, error) {
	o, err := s.conn.q.GetOrgByID(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOrg(o)
	return &out, nil
}

func (s *orgStore) GetByIDIncludeDeleted(ctx context.Context, id string) (*store.Org, error) {
	o, err := s.conn.q.GetOrgByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOrg(o)
	return &out, nil
}

func (s *orgStore) GetByName(ctx context.Context, name string) (*store.Org, error) {
	o, err := s.conn.q.GetOrgByName(ctx, name)
	if err != nil {
		return nil, mapErr(err)
	}
	out := fromDBOrg(o)
	return &out, nil
}

func fromDBOrgs(rows []gendb.Org) []store.Org {
	return store.MapSlice(rows, fromDBOrg)
}

func (s *orgStore) HasAny(ctx context.Context) (bool, error) {
	ok, err := s.conn.q.HasAnyOrg(ctx)
	if err != nil {
		return false, mapErr(err)
	}
	return ok, nil
}

func (s *orgStore) ListAll(ctx context.Context, p store.ListAllOrgsParams) ([]store.Org, error) {
	params, err := listAllOrgsParams(p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.ListAllOrgs(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOrgs(rows), nil
}

func (s *orgStore) Search(ctx context.Context, p store.SearchOrgsParams) ([]store.Org, error) {
	params, err := searchOrgsParams(p.Query, p.Cursor, p.Limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.conn.q.SearchOrgs(ctx, params)
	if err != nil {
		return nil, mapErr(err)
	}
	return fromDBOrgs(rows), nil
}

func (s *orgStore) UpdateName(ctx context.Context, p store.UpdateOrgNameParams) error {
	return mapErr(s.conn.q.UpdateOrgName(ctx, gendb.UpdateOrgNameParams{
		Name: p.Name,
		ID:   p.ID,
	}))
}

func (s *orgStore) SoftDelete(ctx context.Context, id string) error {
	return mapErr(s.conn.q.SoftDeleteOrg(ctx, id))
}

func (s *orgStore) SoftDeleteNonPersonal(ctx context.Context, id string) error {
	return mapErr(s.conn.q.SoftDeleteNonPersonalOrg(ctx, id))
}
