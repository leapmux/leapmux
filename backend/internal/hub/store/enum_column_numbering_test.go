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
// field holds, and none of these columns has a state it could mean. Three of
// them are OPTIONAL and spell absence as SQL NULL instead of an ordinal, as
// agent_input_queue_state.pause_owner does in the worker: an absent
// credential reference or a step-up code holds no kind at all, and a tie CHECK
// keeps its partner column in step. Their range still starts at 1, so a stored
// ordinal is always one the enum declares.
func TestEnumColumnChecksMatchTheirProtoRanges(t *testing.T) {
	t.Parallel()

	for _, c := range enumRangeColumns(t) {
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

func TestAuthorizationCodeCredentialReferencesStayConsistent(t *testing.T) {
	t.Parallel()

	for _, rel := range enumColumnMigrations {
		t.Run(rel, func(t *testing.T) {
			sql := statementsOf(t, rel)
			dialect, _, _ := strings.Cut(rel, string(filepath.Separator))
			emptyStepUp := "step_up_ref_id = ''"
			mintedEmpty := "minted_ref_id = ''"
			if dialect == "mysql" {
				emptyStepUp = "OCTET_LENGTH(step_up_ref_id) = 0"
				mintedEmpty = "OCTET_LENGTH(minted_ref_id) = 0"
			}
			assert.Contains(t, sql, "CHECK ((step_up_kind IS NULL) = ("+emptyStepUp+"))")
			assert.Contains(t, sql,
				"CHECK ("+mintedEmpty+" OR (consumed_at IS NOT NULL AND redemption_kind IS NOT NULL))")
			assert.NotContains(t, sql, "minted_kind")
		})
	}
}

// enumRangeColumn is one enum column's CHECK range, and the proto enum it must
// match.
type enumRangeColumn struct {
	column string
	lastOrdinal,
	lastDeclared int32
	checks []string
}

// enumRangeColumns lists every hub enum column with a CHECK range. Both the
// range test above and the SQLite integer test below read it, so a column
// added here is covered by both.
func enumRangeColumns(t *testing.T) []enumRangeColumn {
	t.Helper()
	return []enumRangeColumn{
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
		{
			column:       "workspace_tab_owned.tab_type",
			lastOrdinal:  int32(leapmuxv1.TabType_TAB_TYPE_IMAGE),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.TabType_name),
			checks:       []string{"CHECK (tab_type BETWEEN 1 AND 4)"},
		},
		{
			column:       "api_tokens.issuance",
			lastOrdinal:  int32(leapmuxv1.ApiTokenIssuance_API_TOKEN_ISSUANCE_ADMIN),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.ApiTokenIssuance_name),
			checks:       []string{"CHECK (issuance BETWEEN 1 AND 3)"},
		},
		{
			// Optional: SQL NULL spells no target, and the tie keeps the id
			// empty exactly then.
			column:       "oauth_authorization_codes.step_up_kind",
			lastOrdinal:  int32(leapmuxv1.CredentialRefKind_CREDENTIAL_REF_KIND_TOKEN),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.CredentialRefKind_name),
			checks:       []string{"CHECK (step_up_kind BETWEEN 1 AND 2)"},
		},
		{
			// Optional: SQL NULL spells a step-up code, and the tie holds a
			// code to exactly one of verifying and redeeming.
			column:       "oauth_authorization_codes.redemption_kind",
			lastOrdinal:  int32(leapmuxv1.RedemptionKind_REDEMPTION_KIND_TOKEN),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.RedemptionKind_name),
			checks: []string{
				"CHECK (redemption_kind BETWEEN 1 AND 2)",
				"CHECK ((redemption_kind IS NULL) = (step_up_kind IS NOT NULL))",
			},
		},
		{
			column:       "workspace_sections.section_type",
			lastOrdinal:  int32(leapmuxv1.SectionType_SECTION_TYPE_BACKGROUND_TASKS),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.SectionType_name),
			checks:       []string{"CHECK (section_type BETWEEN 1 AND 7)"},
		},
		{
			column:       "workspace_sections.sidebar",
			lastOrdinal:  int32(leapmuxv1.Sidebar_SIDEBAR_RIGHT),
			lastDeclared: lastDeclaredOrdinal(t, leapmuxv1.Sidebar_name),
			checks:       []string{"CHECK (sidebar BETWEEN 1 AND 2)"},
		},
	}
}

// SQLite stores any value in an INTEGER column, so `1.5` passes a range CHECK
// and then reads back as an ordinal no Go constant has. Each enum column
// therefore carries a `typeof(col) = 'integer'` guard on its own declaration.
// An OPTIONAL column -- one that spells absence as SQL NULL -- writes the guard
// as `col IS NULL OR typeof(col) = 'integer'`, because typeof(NULL) is 'null';
// both forms refuse a fraction, and this test asserts the guard's presence
// rather than its phrasing. enum_storage_internal_test.go in the sqlite
// package proves the refusal with real writes; this test proves every listed
// column carries it.
func TestSQLiteEnumColumnsStoreIntegersOnly(t *testing.T) {
	t.Parallel()

	statements := statementsOf(t, enumColumnMigrations[0])
	for _, c := range enumRangeColumns(t) {
		t.Run(c.column, func(t *testing.T) {
			table, column, found := strings.Cut(c.column, ".")
			require.True(t, found)
			_, declaration, found := strings.Cut(statements, "CREATE TABLE "+table+" (")
			require.True(t, found, "%s has no CREATE TABLE in the SQLite migration", table)
			declaration, _, found = strings.Cut(declaration, "\n);")
			require.True(t, found)
			var columnDeclaration string
			for _, line := range strings.Split(declaration, "\n") {
				if fields := strings.Fields(line); len(fields) > 1 && fields[0] == column {
					columnDeclaration = line
					break
				}
			}
			require.NotEmpty(t, columnDeclaration, "%s declares no %s column", table, column)
			assert.Contains(t, columnDeclaration, "typeof("+column+") = 'integer'",
				"%s must refuse a non-integer value on its own declaration", c.column)
		})
	}
}

// An ordinal a hub migration spells OUTSIDE a range check.
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
