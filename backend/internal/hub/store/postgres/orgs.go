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
		ID:        o.ID,
		Name:      o.Name,
		CreatedAt: o.CreatedAt.Time,
		DeletedAt: o.DeletedAt.Ptr(),
	}
}

func (s *orgStore) Create(ctx context.Context, p store.CreateOrgParams) error {
	return mapErr(s.conn.q.CreateOrg(ctx, gendb.CreateOrgParams{
		ID: p.ID,
		// An org name mirrors its owner's username, so it shares the username's
		// normalization -- exactly as userStore.Create normalizes the username.
		// Normalizing here (rather than trusting each caller) keeps the mirror an
		// invariant of the store: a caller passing a non-normalized username
		// cannot leave orgs.name and users.username disagreeing in case, which
		// would break RenameUserPersonalOrg's idempotency and, under a
		// case-sensitive collation, the /o/ slug.
		Name: store.NormalizeUsername(p.Name),
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

func (s *orgStore) SoftDelete(ctx context.Context, id string) error {
	return mapErr(s.conn.q.SoftDeleteOrg(ctx, id))
}
