package zcode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeSerializationOutputPathKeepsUntrustedMetadataWithoutOpeningFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*testing.T, zcodeSerializationFixture)
	}{
		{"other strategy", func(_ *testing.T, f zcodeSerializationFixture) { f.metadata["budgetStrategy"] = "truncate" }},
		{"absent path", func(_ *testing.T, f zcodeSerializationFixture) { delete(f.metadata, "artifactPath") }},
		{"empty path", func(_ *testing.T, f zcodeSerializationFixture) { f.metadata["artifactPath"] = "" }},
		{"non-string path", func(_ *testing.T, f zcodeSerializationFixture) { f.metadata["artifactPath"] = false }},
		{"relative path", func(_ *testing.T, f zcodeSerializationFixture) { f.metadata["artifactPath"] = filepath.Base(f.path) }},
		{"foreign session", func(_ *testing.T, f zcodeSerializationFixture) {
			f.metadata["artifactPath"] = filepath.Join(f.location.outputFileRoot, "foreign", filepath.Base(f.path))
		}},
		{"foreign call", func(_ *testing.T, f zcodeSerializationFixture) {
			f.metadata["artifactPath"] = filepath.Join(filepath.Dir(f.path), "other-"+zcodeOutputFileFixtureID+".txt")
		}},
		{"foreign root", func(t *testing.T, f zcodeSerializationFixture) {
			f.metadata["artifactPath"] = filepath.Join(t.TempDir(), "session", filepath.Base(f.path))
		}},
		{"invalid artifact ID", func(_ *testing.T, f zcodeSerializationFixture) {
			f.metadata["artifactPath"] = filepath.Join(filepath.Dir(f.path), "call-tool-result-invalid.txt")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			f := newZCodeSerializationFixture(t)
			test.change(t, f)
			record, err := f.read(t, &zcodeOutputFileCache{})
			require.NoError(t, err)
			assert.Empty(t, record.outputFiles)
		})
	}
}

func TestZCodeSerializationOutputPathDoesNotWaitForATextFile(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	require.NoError(t, os.Remove(f.path))
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.Empty(t, record.outputFiles)
	assert.True(t, record.ready, "native metadata does not wait for an external text file")
}

func TestZCodeSerializationOutputPathDoesNotOpenASymbolicLink(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	outside := filepath.Join(t.TempDir(), "foreign.txt")
	require.NoError(t, os.WriteFile(outside, []byte(f.body), 0o600))
	require.NoError(t, os.Remove(f.path))
	if err := os.Symlink(outside, f.path); err != nil {
		t.Skipf("The host cannot create a symbolic link: %v", err)
	}
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.Empty(t, record.outputFiles)
	assert.True(t, record.ready, "native metadata does not open the symbolic link")
}

func TestZCodeSerializationOutputPathKeepsTheDeclaredFileWhenAnotherCallSharesTheOutputFileID(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	other := filepath.Join(filepath.Dir(f.path), "other-"+zcodeOutputFileFixtureID+".txt")
	require.NoError(t, os.WriteFile(other, []byte("foreign call bytes"), 0o600))
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.True(t, record.ready)
	assert.Empty(t, record.outputFiles, "external text must not enter the stored record")
}

func TestZCodeSerializationOutputPathDoesNotFillTheImageCache(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	cache := &zcodeOutputFileCache{}
	record, err := f.read(t, cache)
	require.NoError(t, err)
	assert.True(t, record.ready)
	assert.Empty(t, record.outputFiles)
	assert.Empty(t, cache.lookup(f.uri), "text metadata must not populate the image cache")
	require.NoError(t, os.Remove(f.path))
	assert.Contains(t, string(record.native.Data), f.path, "the native record retains its path after file removal")
}

func TestZCodeSerializationOutputPathDoesNotReadLiteralDataURIText(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	f.body = "data:text/plain;base64,bGl0ZXJhbCBzY3JpcHQgb3V0cHV0\nnative tail42"
	f.metadata["originalBytes"] = len(f.body)
	require.NoError(t, os.WriteFile(f.path, []byte(f.body), 0o600))
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.True(t, record.ready)
	assert.Empty(t, record.outputFiles, "external text must not enter the stored record")
}

func TestZCodeSerializationOutputPathDoesNotReadAnOversizedTextFile(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	file, err := os.OpenFile(f.path, os.O_WRONLY, 0)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(int64(agent.LiveMaxMessageSize())+1))
	require.NoError(t, file.Close())
	record, readErr := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, readErr)
	assert.Empty(t, record.outputFiles)
	assert.True(t, record.ready)
}

func TestZCodeSerializationOutputPathUsesItsNativePathInsteadOfTheImageRoot(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	f.location.outputFileRoot = filepath.Join(t.TempDir(), "configured-image-root")
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.True(t, record.ready)
	assert.Empty(t, record.outputFiles, "external text must not enter the stored record")
}

func TestZCodeSerializationOutputPathPreservesNativeByteCountMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		count  any
		absent bool
	}{
		{name: "absent count", absent: true},
		{name: "null count", count: nil},
		{name: "negative count", count: -1},
		{name: "boolean count", count: false},
		{name: "string count", count: "42"},
		{name: "fractional count", count: 1.5},
		{name: "count above the message limit", count: agent.LiveMaxMessageSize() + 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newZCodeSerializationFixture(t)
			if tc.absent {
				delete(f.metadata, "originalBytes")
			} else {
				f.metadata["originalBytes"] = tc.count
			}
			record, err := f.read(t, &zcodeOutputFileCache{})
			require.NoError(t, err)
			assert.Empty(t, record.outputFiles, "native byte metadata must not cause external text recovery")
		})
	}
}

func TestZCodeSerializationOutputPathKeepsMismatchedNativeByteMetadata(t *testing.T) {
	t.Parallel()
	f := newZCodeSerializationFixture(t)
	f.metadata["originalBytes"] = len(f.body) + 1
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.Empty(t, record.outputFiles)
	assert.True(t, record.ready)
}

func TestZCodeSerializationOutputPathKeepsZeroAndUnicodeMetadata(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"", "文\n\ufeffnative tail42\n"} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			f := newZCodeSerializationFixture(t)
			f.body = body
			f.metadata["originalBytes"] = len(body)
			require.NoError(t, os.WriteFile(f.path, []byte(body), 0o600))
			record, err := f.read(t, &zcodeOutputFileCache{})
			require.NoError(t, err)
			assert.True(t, record.ready)
			assert.Empty(t, record.outputFiles, "external text must not enter the stored record")
		})
	}
}

func TestZCodeSerializationOutputPathPreservesTheCapturedNativeFilename(t *testing.T) {
	t.Parallel()
	const nativeCall = "tool_0439622a587331469896ac91620"
	const id = "tool-result-99a513fa-2c20-4b2a-bd6b-18a8d6b6d4bc"
	f := newZCodeSerializationFixture(t)
	f.callID = nativeCall
	f.part["callID"] = nativeCall
	f.path = filepath.Join(f.location.outputFileRoot, "session", nativeCall+"-"+id+".txt")
	f.metadata["artifactPath"] = f.path
	record, err := f.read(t, &zcodeOutputFileCache{})
	require.NoError(t, err)
	assert.Contains(t, string(record.native.Data), f.path)
	assert.Empty(t, record.outputFiles, "the native path must not start a text-file read")
	assert.True(t, record.ready)
}
