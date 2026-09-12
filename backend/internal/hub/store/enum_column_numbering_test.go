package store_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Every hub migration, in the three dialects that carry the enum columns.
//
// A migration is frozen history: it cannot take a query parameter the way a
// query in `db/queries/` can, so a number it spells is a copy of a proto
// ordinal that nothing else moves. These tests are what move it.
var enumColumnMigrations = []string{
	filepath.Join("sqlite", "db", "migrations", "00001_initial.sql"),
	filepath.Join("postgres", "db", "migrations", "00001_initial.sql"),
	filepath.Join("mysql", "db", "migrations", "00001_initial.sql"),
}

// Each enum column's CHECK range, and the proto enum it must match.
//
// The upper limit is the LAST declared value in every case, so a new
// enumerator widens the enum without widening the column, and the row that
// carries it fails to insert. That failure is the point: a value a migration
// does not admit needs a migration, not only a proto edit.
//
// The lower limit is 1 in every case, never 0. UNSPECIFIED is what an unset Go
// field holds, and none of these columns has a state it could mean.
func TestEnumColumnChecksMatchTheirProtoRanges(t *testing.T) {
	t.Parallel()

	for _, c := range []struct {
		column string
		lastOrdinal,
		lastDeclared int32
		checks []string
	}{
		{
			column:       "revocation_events.kind",
			lastOrdinal:  int32(leapmuxv1.RevocationEventKind_REVOCATION_EVENT_KIND_USER_INFO),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.RevocationEventKind_name),
			checks:       []string{"CHECK (kind BETWEEN 1 AND 7)"},
		},
		{
			column:       "oauth_clients.registration_source",
			lastOrdinal:  int32(leapmuxv1.AppRegistrationSource_APP_REGISTRATION_SOURCE_DYNAMIC),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.AppRegistrationSource_name),
			checks:       []string{"CHECK (registration_source BETWEEN 1 AND 4)"},
		},
		{
			column:       "oauth_states.purpose",
			lastOrdinal:  int32(leapmuxv1.OAuthStatePurpose_OAUTH_STATE_PURPOSE_REAUTH),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.OAuthStatePurpose_name),
			checks:       []string{"CHECK (purpose BETWEEN 1 AND 2)"},
		},
		{
			column:       "webauthn_sessions.kind",
			lastOrdinal:  int32(leapmuxv1.WebAuthnSessionKind_WEB_AUTHN_SESSION_KIND_RECOVERY),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.WebAuthnSessionKind_name),
			checks:       []string{"CHECK (kind BETWEEN 1 AND 5)"},
		},
		{
			column:       "oauth_providers.provider_type",
			lastOrdinal:  int32(leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.IdentityProviderType_name),
			checks:       []string{"CHECK (provider_type BETWEEN 1 AND 2)"},
		},
		{
			column:       "lifecycle_outbox.op_type",
			lastOrdinal:  int32(leapmuxv1.WorkspaceLifecycleOp_WORKSPACE_LIFECYCLE_OP_DELETE),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.WorkspaceLifecycleOp_name),
			checks:       []string{"CHECK (op_type BETWEEN 1 AND 3)"},
		},
	} {
		t.Run(c.column, func(t *testing.T) {
			assert.Equal(t, c.lastDeclared, c.lastOrdinal,
				"%s: the enum gained a value past the one its CHECK admits -- widen the CHECK in all three migrations", c.column)
			for _, rel := range enumColumnMigrations {
				sql := statementsOf(t, rel)
				for _, check := range c.checks {
					assert.Containsf(t, sql, check, "%s must carry %q for %s", rel, check, c.column)
				}
			}
		})
	}
}

// The one ordinal a hub migration spells OUTSIDE a range check.
//
// idx_revocation_events_session_revoked is a PARTIAL index, and a partial index
// predicate takes no parameter, so this literal cannot follow a renumber the
// way a query's bound argument does. Renumber SESSION_REVOKED and the index
// silently indexes a different kind: the session-close path then seeks rows
// that are not there, and a revoked session's streams stay open.
func TestRevocationEventKindNumbering(t *testing.T) {
	t.Parallel()

	require.Equal(t, leapmuxv1.RevocationEventKind(2),
		leapmuxv1.RevocationEventKind_REVOCATION_EVENT_KIND_SESSION_REVOKED,
		"the partial index below spells this number; renumbering it needs a migration, not just a proto edit")

	// SQLite and PostgreSQL carry it; MySQL has no partial index and filters at
	// read time instead, so it spells no literal here.
	for _, rel := range enumColumnMigrations[:2] {
		sql := statementsOf(t, rel)
		assert.Contains(t, sql,
			"CREATE INDEX idx_revocation_events_session_revoked ON revocation_events(subject_id) WHERE kind = 2",
			"%s must seek SESSION_REVOKED by its ordinal", rel)
	}
}

// No hub migration may go back to storing an enum as a WORD. A reintroduced
// `col IN ('a','b')` is a second numbering by another name: the Go constant and
// the column would drift, and nothing outside this test would report it.
func TestNoHubEnumColumnStoresWords(t *testing.T) {
	t.Parallel()

	for _, rel := range enumColumnMigrations {
		for i, line := range strings.Split(statementsOf(t, rel), "\n") {
			assert.NotContainsf(t, line, " IN ('",
				"%s:%d carries a string-valued enum CHECK; a database enum stores its proto ordinal", rel, i+1)
		}
	}
}

// lastDeclaredOrdinal is the highest ordinal a generated `_name` map holds,
// which is the whole enum rather than the values a test remembered to list.
func lastDeclaredOrdinal(t *testing.T, names map[int32]string) int32 {
	t.Helper()
	require.NotEmpty(t, names)
	var highest int32
	for ordinal := range names {
		if ordinal > highest {
			highest = ordinal
		}
	}
	return highest
}
