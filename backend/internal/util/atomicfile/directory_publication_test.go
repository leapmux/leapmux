package atomicfile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicationThroughSymlinkUsesCanonicalProvenance(t *testing.T) {
	realParent := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	require.NoError(t, os.Symlink(realParent, alias))
	throughAlias := filepath.Join(alias, "new-root", "worker")
	canonicalParent, err := filepath.EvalSymlinks(realParent)
	require.NoError(t, err)
	canonical := filepath.Join(canonicalParent, "new-root", "worker")
	require.NoError(t, MkdirAllDurable(throughAlias))
	require.NoError(t, MkdirAllDurable(canonical), "a later canonical lease must accept publication created through an alias")
	require.NoError(t, MkdirAllDurable(throughAlias))
	record, err := loadRecordAt(recordPathAt(filepath.Join(canonicalParent, "new-root")))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(canonicalParent, "new-root"), record.Directory)
	assert.Equal(t, canonical, record.TargetDirectory)
}

// canonicalTempDir gives filesystem observers the same identity publication uses.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return path
}

// writeRawRecord writes record bytes verbatim, so a test can plant a
// malformed or hostile record without the validation in the way.
func writeRawRecord(t *testing.T, path string, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, WriteFile(path, []byte(content), 0o600))
}

func TestNestedPublicationRecordDoesNotHideInvalidAncestor(t *testing.T) {
	outer := filepath.Join(t.TempDir(), "outer")
	inner := filepath.Join(outer, "worker")
	writeRawRecord(t, recordPathAt(outer), "account_id: [invalid]\nphase: prepared\n")
	record, err := yamlMarshalRecord(&publicationRecord{
		AccountID: currentAccountID(), Directory: inner,
		TargetDirectory: inner, Phase: phaseCommitted,
	})
	require.NoError(t, err)
	writeRawRecord(t, recordPathAt(inner), string(record))
	target := filepath.Join(inner, "sessions")
	require.Error(t, MkdirAllDurable(target))
	assert.NoDirExists(t, target)
	content, err := os.ReadFile(recordPathAt(outer))
	require.NoError(t, err)
	assert.Equal(t, "account_id: [invalid]\nphase: prepared\n", string(content))
}

// TestPublicationRecordValidationAndConcurrency covers the record
// rejections: a linked record, another account's record, malformed
// fields, a record naming another directory, and a record whose target
// escapes its own directory. Each is rejected BEFORE the subtree is
// modified: the record's bytes stay, and no staging tree appears.
func TestPublicationRecordValidationAndConcurrency(t *testing.T) {
	t.Run("a symlinked record is rejected", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "leapmux")
		require.NoError(t, os.MkdirAll(root, 0o700))
		// The link target is a benign record; the LINK is the defect.
		real := filepath.Join(base, "elsewhere.yaml")
		require.NoError(t, os.WriteFile(real, nil, 0o600))
		require.NoError(t, os.Symlink(real, recordPathAt(root)))

		err := MkdirAllDurable(filepath.Join(root, "worker"))
		require.Error(t, err)
		assert.NoFileExists(t, filepath.Join(root, "worker"))
	})

	t.Run("another account's record is rejected", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "leapmux")
		require.NoError(t, os.MkdirAll(root, 0o700))
		writeRawRecord(t, recordPathAt(root), "account_id: \"1\"\ndirectory: "+root+
			"\ntarget_directory: "+filepath.Join(root, "worker")+"\nphase: committed\n")

		err := MkdirAllDurable(filepath.Join(root, "worker"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("malformed fields are rejected", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "leapmux")
		require.NoError(t, os.MkdirAll(root, 0o700))
		writeRawRecord(t, recordPathAt(root), "account_id: 501\ndirectory: [not, a, string]\n")

		err := MkdirAllDurable(filepath.Join(root, "worker"))
		require.Error(t, err)
	})

	t.Run("a record naming another directory is rejected", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "leapmux")
		require.NoError(t, os.MkdirAll(root, 0o700))
		writeRawRecord(t, recordPathAt(root), "account_id: \""+currentAccountID()+"\"\ndirectory: "+filepath.Join(base, "other")+
			"\ntarget_directory: "+filepath.Join(base, "other", "x")+"\nphase: committed\n")

		err := MkdirAllDurable(filepath.Join(root, "worker"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "names directory")
	})

	t.Run("a target outside its directory is rejected", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "leapmux")
		require.NoError(t, os.MkdirAll(root, 0o700))
		writeRawRecord(t, recordPathAt(root), "account_id: \""+currentAccountID()+"\"\ndirectory: "+root+
			"\ntarget_directory: "+filepath.Join(base, "escape")+"\nphase: committed\n")

		err := MkdirAllDurable(filepath.Join(root, "worker"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "outside")
	})

	t.Run("an unknown phase is rejected", func(t *testing.T) {
		base := t.TempDir()
		root := filepath.Join(base, "leapmux")
		require.NoError(t, os.MkdirAll(root, 0o700))
		writeRawRecord(t, recordPathAt(root), "account_id: \""+currentAccountID()+"\"\ndirectory: "+root+
			"\ntarget_directory: "+filepath.Join(root, "worker")+"\nphase: maybe\n")

		err := MkdirAllDurable(filepath.Join(root, "worker"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "phase")
	})
}

// A committed record makes the subtree plain reusable storage: a deeper
// creation publishes only the new part and keeps the original
// provenance.
func TestMkdirAllDurableReusesACommittedRecord(t *testing.T) {
	base := canonicalTempDir(t)
	root := filepath.Join(base, "leapmux", "worker")
	require.NoError(t, MkdirAllDurable(root))

	original, err := loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)
	require.Equal(t, phaseCommitted, original.Phase)

	deeper := filepath.Join(root, "sessions", "today")
	require.NoError(t, MkdirAllDurable(deeper))
	assert.DirExists(t, deeper)

	again, err := loadRecordAt(recordPathAt(filepath.Join(base, "leapmux")))
	require.NoError(t, err)
	assert.Equal(t, original, again, "a deeper creation leaves the committed provenance untouched")
}

// A repeated call is a no-op on a confirmed tree.
func TestMkdirAllDurableIsIdempotent(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "a", "b", "c")
	require.NoError(t, MkdirAllDurable(target))
	require.NoError(t, MkdirAllDurable(target))

	entries, err := os.ReadDir(filepath.Join(base, "a"))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{publicationRecordName, "b"}, names,
		"no staging tree survives the repeat")
}

// marshalRecordQuotedAccount pins the exact record shape: the account id
// renders as a quoted string, so a numeric uid never round-trips as an
// int and changes the file's shape.
func TestRecordMarshalsTheAccountAsString(t *testing.T) {
	data, err := yamlMarshalRecord(&publicationRecord{
		AccountID:       "501",
		Directory:       "/u/leapmux",
		TargetDirectory: "/u/leapmux/cache",
		Phase:           phasePrepared,
	})
	require.NoError(t, err)
	assert.Contains(t, string(data), `account_id: "501"`)
}
