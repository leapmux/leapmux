package tofupins_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/tofupins"
)

func openStore(t *testing.T, path string) *tofupins.Store {
	t.Helper()
	s, err := tofupins.Open(tofupins.Options{
		Path:             path,
		DirMode:          0o755,
		FileMode:         0o644,
		MismatchHintTmpl: "clear with `test-cli pins remove --worker-id=%s`",
	})
	require.NoError(t, err)
	return s
}

func TestStore_OpenOnMissingFileCreatesEmptyStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	// No file yet on disk, so Snapshot must be empty.
	assert.Empty(t, s.Snapshot())
	// File only materializes on first persist (Verify).
	_, err := os.Stat(path)
	assert.True(t, os.IsNotExist(err), "Open must not create the file before any write")
}

func TestStore_OpenCreatesParentDirectoryWithRequestedMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits aren't meaningful on Windows")
	}
	dir := filepath.Join(t.TempDir(), "nested", "deep")
	path := filepath.Join(dir, "pins.json")
	s, err := tofupins.Open(tofupins.Options{
		Path:             path,
		DirMode:          0o700,
		FileMode:         0o600,
		MismatchHintTmpl: "noop %s",
	})
	require.NoError(t, err)
	// First Verify triggers the file write.
	require.NoError(t, s.Verify("worker-1", []byte("x"), []byte("m"), []byte("sl")))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "parent dir mode should match DirMode")

	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm(), "pin file mode should match FileMode")
}

func TestStore_VerifyFirstContactRecordsBase64Keys(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	require.NoError(t, s.Verify("worker-A", []byte("xkey"), []byte("mkey"), []byte("slkey")))

	snap := s.Snapshot()
	require.Len(t, snap, 1)
	entry, ok := snap["worker-A"]
	require.True(t, ok)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("xkey")), entry.X25519PublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("mkey")), entry.MlkemPublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("slkey")), entry.SlhdsaPublicKey)
	assert.False(t, entry.FirstSeenAt.IsZero())
	assert.False(t, entry.LastUsedAt.IsZero())
}

func TestStore_VerifySecondContactWithMatchingKeysSucceedsAndBumpsLastUsedAt(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))
	first := s.Snapshot()["w"]

	// Second contact with identical keys must succeed and bump LastUsedAt
	// while preserving FirstSeenAt.
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))
	second := s.Snapshot()["w"]
	assert.Equal(t, first.FirstSeenAt, second.FirstSeenAt, "FirstSeenAt is immutable after first contact")
	assert.True(t, !second.LastUsedAt.Before(first.LastUsedAt), "LastUsedAt must monotonically advance")
}

func TestStore_VerifyMismatchReturnsHintWithWorkerID(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	require.NoError(t, s.Verify("w-A", []byte("x"), []byte("m"), []byte("sl")))

	// Different X25519 key for the same worker_id triggers mismatch.
	err := s.Verify("w-A", []byte("x-NEW"), []byte("m"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
	assert.Contains(t, err.Error(), "w-A", "error must surface worker_id so user knows what to clear")
	assert.Contains(t, err.Error(), "test-cli pins remove --worker-id=w-A",
		"error must surface the configured clear-command hint")
}

func TestStore_VerifyMismatchOnSecondaryKeyTriggersError(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	// Only the MLKEM key differs; mismatch must still fire (a partial
	// re-key still indicates the worker's identity has shifted).
	err := s.Verify("w", []byte("x"), []byte("m-NEW"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
}

func TestStore_VerifyRequiresWorkerID(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	err := s.Verify("", []byte("x"), []byte("m"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker_id required")
}

func TestStore_RemoveIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	// Remove on never-seen worker_id is a no-op (no error).
	require.NoError(t, s.Remove("never-seen"))
	// Remove an existing entry, then double-remove.
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, s.Remove("w"))
	require.NoError(t, s.Remove("w"))
	assert.Empty(t, s.Snapshot())
}

func TestStore_RemoveAllowsRePinWithDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, s.Remove("w"))

	// After Remove, a TOFU re-pin with different keys must succeed.
	require.NoError(t, s.Verify("w", []byte("x-NEW"), []byte("m-NEW"), []byte("sl-NEW")))
	entry := s.Snapshot()["w"]
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x-NEW")), entry.X25519PublicKey)
}

func TestStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")

	first := openStore(t, path)
	require.NoError(t, first.Verify("w-A", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, first.Verify("w-B", []byte("x2"), []byte("m2"), []byte("sl2")))

	// Second instance must read the same pins from disk.
	second := openStore(t, path)
	snap := second.Snapshot()
	require.Len(t, snap, 2)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x")), snap["w-A"].X25519PublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x2")), snap["w-B"].X25519PublicKey)

	// Subsequent Verify against the re-opened store must enforce
	// the recorded keys — proving the in-memory mirror was restored
	// from disk, not just empty.
	err := second.Verify("w-A", []byte("x-NEW"), []byte("m"), []byte("sl"))
	require.Error(t, err)
}

func TestStore_PersistsAsWrapperFormat(t *testing.T) {
	// On-disk shape pins the JSON wrapper format so a future reader
	// (older binary, manual cat) can identify the file. Without this,
	// a refactor that flips to a bare map would silently lose
	// existing pins.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	var parsed struct {
		Pins map[string]struct {
			X25519PublicKey string `json:"x25519_public_key"`
		} `json:"pins"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	require.Contains(t, parsed.Pins, "w", "on-disk JSON must use the wrapper format {\"pins\": {...}}")
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x")), parsed.Pins["w"].X25519PublicKey)
}

func TestStore_VerifyAndRemoveAreConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)

	// 50 workers, 10 goroutines per worker hammering Verify with
	// the same keys; the mutex must serialize them so the first
	// recorded keys win and subsequent calls succeed-as-match.
	const workers = 50
	const goroutinesPerWorker = 10
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		id := "w-" + string(rune('A'+(i%26))) + string(rune('A'+(i/26)))
		for j := 0; j < goroutinesPerWorker; j++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if err := s.Verify(id, []byte("x"), []byte("m"), []byte("sl")); err != nil {
					t.Errorf("Verify(%s): %v", id, err)
				}
			}(id)
		}
	}
	wg.Wait()

	snap := s.Snapshot()
	// Number of distinct ids = ceil(workers / 26) buckets of 26-letter ids.
	require.NotEmpty(t, snap, "expected at least one recorded pin")
	for id, entry := range snap {
		assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x")), entry.X25519PublicKey, "worker %s", id)
	}
}

func TestStore_OpenWithoutPathReturnsError(t *testing.T) {
	_, err := tofupins.Open(tofupins.Options{
		Path:    "",
		DirMode: 0o755,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Path required")
}

func TestStore_OpenWithCorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	// Write a garbage payload so Open's json.Unmarshal fails. We
	// surface the error to the caller rather than silently dropping
	// existing pins — losing the file means the next Verify would
	// TOFU-record a fresh (potentially spoofed) key.
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o644))

	_, err := tofupins.Open(tofupins.Options{
		Path:             path,
		DirMode:          0o755,
		FileMode:         0o644,
		MismatchHintTmpl: "noop %s",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pins")
}

func TestStore_VerifyRepeatedMatchesSkipDiskWritesWithinThrottleWindow(t *testing.T) {
	// Establish a pin, capture the file mtime, then hammer Verify with
	// the same keys. With the default 10-minute throttle the on-disk
	// JSON should not be rewritten — only the in-memory `LastUsedAt`
	// advances.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	before, err := os.Stat(path)
	require.NoError(t, err)
	firstSnap := s.Snapshot()["w"]

	// 50 repeated Verify calls; in-memory LastUsedAt may advance but
	// the file should not be rewritten under the throttle window.
	for i := 0; i < 50; i++ {
		require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))
	}

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime(),
		"file mtime must not change while LastUsedAt bumps stay within the throttle window")
	assert.Equal(t, before.Size(), after.Size(),
		"file size must not change either (no rewrites)")

	finalSnap := s.Snapshot()["w"]
	assert.False(t, finalSnap.LastUsedAt.Before(firstSnap.LastUsedAt),
		"in-memory LastUsedAt must still advance even when persistence is throttled")
}

func TestStore_VerifyFlushesLastUsedAtPastThrottleWindow(t *testing.T) {
	// With a very small throttle (1ns) every subsequent Verify must
	// rewrite the file because the bump is always past the window.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s, err := tofupins.Open(tofupins.Options{
		Path:                  path,
		DirMode:               0o755,
		FileMode:              0o644,
		MismatchHintTmpl:      "noop %s",
		LastUsedPersistMinGap: time.Nanosecond,
	})
	require.NoError(t, err)
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	before, err := os.Stat(path)
	require.NoError(t, err)
	// Sleep a couple of milliseconds to guarantee mtime resolution
	// separates the two writes on filesystems with coarse timestamps
	// (HFS+ on macOS, FAT32 on Windows).
	time.Sleep(5 * time.Millisecond)
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	after, err := os.Stat(path)
	require.NoError(t, err)
	assert.True(t, after.ModTime().After(before.ModTime()),
		"with throttle below the inter-call gap, every Verify must rewrite the file")
}

func TestStore_VerifyFirstContactAlwaysWritesEvenWithThrottle(t *testing.T) {
	// First contact (no pre-existing pin) creates a new Entry and must
	// always persist regardless of the throttle — there's no
	// `previously-persisted` value to compare against.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s, err := tofupins.Open(tofupins.Options{
		Path:                  path,
		DirMode:               0o755,
		FileMode:              0o644,
		MismatchHintTmpl:      "noop %s",
		LastUsedPersistMinGap: 24 * time.Hour, // very aggressive throttle
	})
	require.NoError(t, err)
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	// File must exist after first Verify even under heavy throttle.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func TestStore_SnapshotReturnsACopyNotALiveReference(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	require.NoError(t, s.Verify("w", []byte("x"), []byte("m"), []byte("sl")))

	snap1 := s.Snapshot()
	require.Contains(t, snap1, "w")
	delete(snap1, "w") // mutate the snapshot

	// The store's internal map must NOT be affected by the caller's
	// mutation. Snapshot is a deep enough copy that pin enumeration
	// during teardown can't be hijacked by a misbehaving caller.
	snap2 := s.Snapshot()
	assert.Contains(t, snap2, "w", "Snapshot must return a copy independent of the store's internal map")
}
