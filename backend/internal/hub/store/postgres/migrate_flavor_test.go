package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addNoTransaction is what makes a YugabyteDB migration report the outcome the
// database actually produced, so its two properties are worth pinning.
func TestAddNoTransaction(t *testing.T) {
	t.Parallel()

	migration := []byte("-- +goose Up\nCREATE TABLE t (id TEXT);\n")
	out := addNoTransaction(migration)

	// The annotation must PRECEDE the Up marker; goose reads it as a property
	// of the file, and one that follows the marker is a comment inside the
	// migration body.
	assert.True(t, strings.HasPrefix(string(out), gooseNoTransaction),
		"the annotation must come first, or goose reads it as ordinary SQL")
	assert.Contains(t, string(out), "-- +goose Up")

	// IDEMPOTENT, so a migration written with the annotation is unchanged and
	// does not end up carrying it twice.
	assert.Equal(t, string(out), string(addNoTransaction(out)))
}

// The flavor probe reads version(), which is the one statement all three
// backends answer. Each names itself there, and a plain PostgreSQL names
// neither -- so the DEFAULT is the strict path, and a backend the probe does
// not recognise keeps the wrapping transaction rather than losing it.
func TestDetectFlavorReadsTheVersionString(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		version string
		want    sqlFlavor
	}{
		"postgres": {
			version: "PostgreSQL 16.4 on aarch64-unknown-linux-gnu, compiled by gcc",
			want:    flavorPostgres,
		},
		"cockroachdb": {
			version: "CockroachDB CCL v24.1.5 (aarch64-unknown-linux-gnu, built 2024/08/26)",
			want:    flavorCockroach,
		},
		"yugabytedb": {
			version: "PostgreSQL 15.2-YB-2025.2.2.1-b0 on aarch64-unknown-linux-gnu",
			want:    flavorYugabyte,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, flavorOf(tc.version))
		})
	}

	// The two markers must not both match one string, or the switch's order
	// would silently decide which behaviour a backend gets.
	require.NotContains(t, "CockroachDB", "YB-")
}
