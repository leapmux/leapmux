package agent

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// absPath builds an absolute path for the RUNNING OS out of segments.
//
// A POSIX literal cannot stand in for one. An absolute path on Windows carries
// a volume, so filepath.IsAbs(`/usr/bin/node`) and filepath.IsAbs(`\tmp\x`) are
// both FALSE there, and filepath.Clean rewrites the separators of either. A
// fixture that is absolute on POSIX alone therefore states a path the host can
// never produce, and every reader that compares the exact bytes then answers
// nothing -- correctly, and for a reason the assertion does not show:
//
//   - The SQL session readers bind filepath.Clean of the working directory and
//     match it against the stored column. A query cleaned to `\` never equals a
//     row spelled with `/`.
//   - Cursor hashes the cleaned working directory into a directory name, so the
//     two spellings name two different directories.
//   - Reasonix folds the case of its workspace slug on Windows.
//   - storeOverridePath and gooseDBCandidates ask filepath.IsAbs whether an
//     environment override is a whole path or a bare name.
func absPath(segments ...string) string {
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	return filepath.Join(append([]string{root}, segments...)...)
}

// absPath is only useful if what it returns is absolute HERE, and the OS that
// gets that wrong is the one no developer runs the suite on. This case is what
// makes the Windows runner check it.
func TestAbsPath_IsAbsoluteOnTheRunningOS(t *testing.T) {
	t.Parallel()

	assert.True(t, filepath.IsAbs(absPath("usr", "bin", "node")),
		"absPath must produce an absolute path on %s", runtime.GOOS)

	// The second half of the contract, and the one the SQL fixtures rest on:
	// the readers bind filepath.Clean of the working directory and match the
	// stored column byte for byte, so a path that Clean would rewrite seeds a
	// row no query can reach.
	dir := absPath("Users", "dev", "project")
	assert.Equal(t, dir, filepath.Clean(dir), "a fixture path must already be in cleaned form")

	if runtime.GOOS == "windows" {
		assert.Equal(t, "C:", filepath.VolumeName(dir),
			"an absolute path on Windows carries a volume, which is what a POSIX literal lacks")
	}
}

// requireHostAbsDir fails a fixture at its own call site when the working
// directory it states is not absolute for the host.
//
// Without it the failure appears far from the mistake and describes the wrong
// thing: no query matches the seeded row, the reader answers nothing, and the
// assertion that fails names a filter that is correct.
func requireHostAbsDir(t *testing.T, dir string) {
	t.Helper()
	require.Truef(t, filepath.IsAbs(dir),
		"a fixture working directory must be absolute on %s; build it with absPath: %q", runtime.GOOS, dir)
}

// fixtureJSONString renders a value as a JSON string literal, quotation marks
// included, for a fixture that states its document as raw text.
//
// A host path is what needs it. Pasted between two quotation marks, a Windows
// path is not valid JSON: `\U` is an unknown escape, and the decoder rejects
// the WHOLE document rather than that one field, so the session it describes
// disappears and no assertion says why. A JSON string is also a valid YAML
// double-quoted scalar, which is why Copilot's YAML fixture uses this too.
//
// json.Marshal cannot fail for a string, so the error path is unreachable.
func fixtureJSONString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func TestFixtureJSONString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, `"plain"`, fixtureJSONString("plain"))

	// The case the helper exists for, stated as a literal so it is exercised on
	// every OS: a Windows path decodes back to itself.
	windows := `C:\Users\dev\project`
	var got string
	require.NoError(t, json.Unmarshal([]byte(fixtureJSONString(windows)), &got))
	assert.Equal(t, windows, got)

	// And the surrounding document stays decodable, which is what a fixture
	// that pasted the raw path lost.
	var record struct {
		Cwd string `json:"cwd"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"cwd":`+fixtureJSONString(windows)+`}`), &record))
	assert.Equal(t, windows, record.Cwd)

	var broken struct {
		Cwd string `json:"cwd"`
	}
	assert.Error(t, json.Unmarshal([]byte(`{"cwd":"`+windows+`"}`), &broken),
		"the unescaped form is what this helper replaces")
}
