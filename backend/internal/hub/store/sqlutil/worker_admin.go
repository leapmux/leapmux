package sqlutil

import (
	"database/sql"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// WorkerAdminFields holds the driver-agnostic fields needed to build a
// store.WorkerWithOwner. Each SQL backend converts its driver-specific
// row type into this struct, then calls ToWorkerWithOwner.
type WorkerAdminFields struct {
	ID, AuthToken, RegisteredBy                string
	Status                                     leapmuxv1.WorkerStatus
	CreatedAt                                  time.Time
	LastSeenAt, DeletedAt                      *time.Time
	PublicKey, MlkemPublicKey, SlhdsaPublicKey []byte
	OwnerUsername                              string
}

// ToWorkerWithOwner converts the fields into a store.WorkerWithOwner.
func (f WorkerAdminFields) ToWorkerWithOwner() store.WorkerWithOwner {
	return store.WorkerWithOwner{
		Worker: store.Worker{
			ID:              f.ID,
			AuthToken:       f.AuthToken,
			RegisteredBy:    f.RegisteredBy,
			Status:          f.Status,
			CreatedAt:       f.CreatedAt,
			LastSeenAt:      f.LastSeenAt,
			PublicKey:       f.PublicKey,
			MlkemPublicKey:  f.MlkemPublicKey,
			SlhdsaPublicKey: f.SlhdsaPublicKey,
			DeletedAt:       f.DeletedAt,
		},
		OwnerUsername: f.OwnerUsername,
	}
}

// WorkerAdminRow is satisfied by all sqlc-generated ListWorkersAdmin*Row
// types that use database/sql types (SQLite, MySQL). PostgreSQL uses
// pgtype and must convert via WorkerAdminFields directly.
type WorkerAdminRow interface {
	~struct {
		ID              string                 `json:"id"`
		AuthToken       string                 `json:"auth_token"`
		RegisteredBy    string                 `json:"registered_by"`
		Status          leapmuxv1.WorkerStatus `json:"status"`
		CreatedAt       time.Time              `json:"created_at"`
		LastSeenAt      sql.NullTime           `json:"last_seen_at"`
		PublicKey       []byte                 `json:"public_key"`
		MlkemPublicKey  []byte                 `json:"mlkem_public_key"`
		SlhdsaPublicKey []byte                 `json:"slhdsa_public_key"`
		DeletedAt       sql.NullTime           `json:"deleted_at"`
		OwnerUsername   string                 `json:"owner_username"`
	}
}

// FromDBWorkersAdmin converts sqlc-generated admin worker rows (database/sql
// types) to store.WorkerWithOwner slices.
func FromDBWorkersAdmin[R WorkerAdminRow](rows []R) []store.WorkerWithOwner {
	return MapSlice(rows, func(r R) store.WorkerWithOwner {
		// concrete must mirror WorkerAdminRow exactly (Go generics workaround).
		type concrete struct {
			ID              string                 `json:"id"`
			AuthToken       string                 `json:"auth_token"`
			RegisteredBy    string                 `json:"registered_by"`
			Status          leapmuxv1.WorkerStatus `json:"status"`
			CreatedAt       time.Time              `json:"created_at"`
			LastSeenAt      sql.NullTime           `json:"last_seen_at"`
			PublicKey       []byte                 `json:"public_key"`
			MlkemPublicKey  []byte                 `json:"mlkem_public_key"`
			SlhdsaPublicKey []byte                 `json:"slhdsa_public_key"`
			DeletedAt       sql.NullTime           `json:"deleted_at"`
			OwnerUsername   string                 `json:"owner_username"`
		}
		c := concrete(r)
		return WorkerAdminFields{
			ID:              c.ID,
			AuthToken:       c.AuthToken,
			RegisteredBy:    c.RegisteredBy,
			Status:          c.Status,
			CreatedAt:       c.CreatedAt,
			LastSeenAt:      NullTimeToPtr(c.LastSeenAt),
			PublicKey:       c.PublicKey,
			MlkemPublicKey:  c.MlkemPublicKey,
			SlhdsaPublicKey: c.SlhdsaPublicKey,
			DeletedAt:       NullTimeToPtr(c.DeletedAt),
			OwnerUsername:   c.OwnerUsername,
		}.ToWorkerWithOwner()
	})
}
