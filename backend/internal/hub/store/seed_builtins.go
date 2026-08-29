package store

import (
	"context"
	"fmt"
	"time"

	"github.com/leapmux/leapmux/internal/hub/oauthapp"
)

// SeedBuiltIns seeds the two registrations this build ships with, and
// reconciles them on every later boot.
//
// It is the ONE writer of a built-in's constant fields. The values live in
// internal/hub/oauthapp -- the package every reader already imports -- so an
// edit there, and nothing else, changes what a built-in registration carries.
// Every store open runs this right after migration, which gives it three
// properties the migration's INSERT could not have:
//
//   - A build that changes a constant reaches EXISTING deployments on their
//     next restart, with no new migration.
//   - A row an older build seeded (or a hand-edited one) is reconciled to
//     the current constants, so the three dialects cannot drift apart.
//   - The upsert's conflict branch rewrites ONLY the constant columns:
//     elevation_allowed -- the one field an operator may change on a
//     built-in -- keeps the operator's value, and the vouch, a revocation
//     and the row's own created_at stay as the last writer left them.
//
// Idempotent: a store that already holds the current constants writes nothing
// observable, so calling it on every boot is free.
func SeedBuiltIns(ctx context.Context, st Store, now time.Time) error {
	seed := func(p UpsertBuiltInClientParams) UpsertBuiltInClientParams {
		p.RegistrationSource = OAuthClientSourceBuiltin
		// TRUE against the column's FALSE default, so `control admin ...`
		// works on a fresh database: seeding it off would take a working
		// capability away. A later SetAppElevationAllowed is the operator's
		// decision, and the conflict branch never touches the column again.
		p.ElevationAllowed = true
		p.CreatedAt, p.UpdatedAt = now, now
		return p
	}
	rows := []UpsertBuiltInClientParams{
		seed(UpsertBuiltInClientParams{
			ClientID:     oauthapp.ControlCLIClientID,
			ClientName:   oauthapp.ControlCLIName,
			ClientURI:    "",
			RedirectURIs: oauthapp.ControlCLIRedirectURI,
			Scopes:       oauthapp.ControlCLIScopes,
			GrantTypes:   oauthapp.ControlCLIGrantTypes,
		}),
		seed(UpsertBuiltInClientParams{
			// No redirect URI and no grant type, so it runs no flow: the row
			// only names WHO holds an out-of-band administrator-issued
			// credential.
			ClientID:   oauthapp.ServiceAccountClientID,
			ClientName: oauthapp.ServiceAccountName,
			ClientURI:  "",
			Scopes:     oauthapp.ServiceAccountScopes,
		}),
	}
	for _, row := range rows {
		if err := st.OAuthClients().UpsertBuiltIn(ctx, row); err != nil {
			return fmt.Errorf("seed built-in app %s: %w", row.ClientID, err)
		}
	}
	return nil
}

// MigratorWithBuiltIns wraps a dialect's Migrator so that a completed
// migration also seeds and reconciles the built-in registrations.
//
// Migrate-then-seed is the boot sequence, and it belongs in ONE place per
// store: `Open` runs it for production, and every caller that migrates an
// already-constructed store -- the integration suites, which build a Store
// from a pool first and run `st.Migrator().Migrate(ctx)` after -- completes
// the same boot through this wrapper. A bare `newFromPool` cannot seed,
// because the pool it wraps has not necessarily been migrated yet; making the
// wrapper the dialects' one Migrator surface is what removes that ordering
// question entirely.
//
// MigrateTo stays bare deliberately: it is the migrator tests' and an
// operator's rollback knife, and half-completing a rollback target is not a
// boot.
func MigratorWithBuiltIns(inner Migrator, st Store) Migrator {
	return migratorWithBuiltIns{inner: inner, st: st}
}

type migratorWithBuiltIns struct {
	inner Migrator
	st    Store
}

func (m migratorWithBuiltIns) CurrentVersion(ctx context.Context) (int64, error) {
	return m.inner.CurrentVersion(ctx)
}

func (m migratorWithBuiltIns) LatestVersion() int64 { return m.inner.LatestVersion() }

func (m migratorWithBuiltIns) Migrate(ctx context.Context) error {
	if err := m.inner.Migrate(ctx); err != nil {
		return err
	}
	return SeedBuiltIns(ctx, m.st, time.Now())
}

func (m migratorWithBuiltIns) MigrateTo(ctx context.Context, version int64) error {
	return m.inner.MigrateTo(ctx, version)
}
