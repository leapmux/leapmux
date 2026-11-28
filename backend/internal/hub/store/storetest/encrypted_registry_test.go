package storetest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v4"

	"github.com/leapmux/leapmux/internal/hub/store/sqlutil"
)

type sqlcConfigDoc struct {
	SQL []struct {
		Gen struct {
			Go struct {
				Overrides []struct {
					Column   string `yaml:"column"`
					Nullable bool   `yaml:"nullable"`
					GoType   struct {
						Import string `yaml:"import"`
						Type   string `yaml:"type"`
					} `yaml:"go_type"`
				} `yaml:"overrides"`
			} `yaml:"go"`
		} `yaml:"gen"`
	} `yaml:"sql"`
}

// TestEncryptedRegistryMatchesSqlcOverrides verifies that
// sqlutil.PersistentEncryptedTables union sqlutil.TransientEncryptedTables
// matches every keystore.Ciphertext and keystore.NullCiphertext column override
// across sqlite, postgres, and mysql sqlc.yaml configurations, including exact
// nullability.
func TestEncryptedRegistryMatchesSqlcOverrides(t *testing.T) {
	t.Parallel()

	const keystoreImport = "github.com/leapmux/leapmux/internal/hub/keystore"

	expected := map[string]bool{} // "table.column" -> nullable
	for _, tbl := range sqlutil.PersistentEncryptedTables {
		for _, col := range tbl.Columns {
			key := tbl.Table + "." + col.Name
			_, dup := expected[key]
			require.Falsef(t, dup, "duplicate encrypted column %q in registry", key)
			expected[key] = col.Nullable
		}
	}
	for _, tbl := range sqlutil.TransientEncryptedTables {
		for _, col := range tbl.Columns {
			key := tbl.Table + "." + col.Name
			_, dup := expected[key]
			require.Falsef(t, dup, "duplicate encrypted column %q in registry", key)
			expected[key] = col.Nullable
		}
	}
	require.Len(t, expected, 39, "expected 39 encrypted columns across 17 tables")

	for _, dialect := range hubDialects {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()

			data, err := os.ReadFile(filepath.Join("..", dialect, "sqlc.yaml"))
			require.NoError(t, err)

			var doc sqlcConfigDoc
			require.NoError(t, yaml.Unmarshal(data, &doc))
			require.Len(t, doc.SQL, 1)

			nonNullTypes := map[string]string{}
			nullableTypes := map[string]string{}
			for _, ov := range doc.SQL[0].Gen.Go.Overrides {
				if ov.GoType.Import != keystoreImport {
					continue
				}
				require.NotEmpty(t, ov.Column)
				if ov.Nullable {
					nullableTypes[ov.Column] = ov.GoType.Type
				} else {
					nonNullTypes[ov.Column] = ov.GoType.Type
				}
			}

			// Every sqlc keystore override must exist in the registry with matching nullability.
			for col, typ := range nonNullTypes {
				wantNullable, ok := expected[col]
				require.Truef(t, ok, "%s/sqlc.yaml overrides %q to %s, which is missing from encrypted_columns.go", dialect, col, typ)
				if wantNullable {
					assert.Equalf(t, "NullCiphertext", typ, "%s/sqlc.yaml non-null override for nullable %q", dialect, col)
					assert.Equalf(t, "NullCiphertext", nullableTypes[col], "%s/sqlc.yaml must also declare nullable: true NullCiphertext for %q", dialect, col)
				} else {
					assert.Equalf(t, "Ciphertext", typ, "%s/sqlc.yaml override for non-null %q", dialect, col)
					_, hasNullable := nullableTypes[col]
					assert.Falsef(t, hasNullable, "%s/sqlc.yaml unexpectedly declares nullable: true for non-null %q", dialect, col)
				}
			}
			for col := range nullableTypes {
				_, ok := expected[col]
				assert.Truef(t, ok, "%s/sqlc.yaml nullable override %q is missing from encrypted_columns.go", dialect, col)
			}

			// Every registry column must exist in sqlc.yaml.
			for col := range expected {
				_, ok := nonNullTypes[col]
				assert.Truef(t, ok, "encrypted_columns.go declares %q, which is missing from %s/sqlc.yaml", col, dialect)
			}
		})
	}
}
