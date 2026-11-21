package tofupins_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/noise"
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

// resolveCommit runs one full Resolve through its commit, the
// transaction that replaces the old one-shot Verify.
func resolveCommit(ctx context.Context, s *tofupins.Store, workerID string, x, m, sl []byte) error {
	commit, err := s.Resolve(ctx, workerID, x, m, sl)
	if err != nil {
		return err
	}
	return commit(ctx)
}

func snapshot(t *testing.T, s *tofupins.Store) map[string]tofupins.Entry {
	t.Helper()
	snap, err := s.Snapshot(t.Context())
	require.NoError(t, err)
	return snap
}

// openStoreAt returns a store whose clock is the variable clock, which
// the test advances between calls to place them relative to the
// throttle window without sleeping.
func openStoreAt(t *testing.T, path string, clock *time.Time) *tofupins.Store {
	t.Helper()
	s, err := tofupins.Open(tofupins.Options{
		Path:             path,
		DirMode:          0o755,
		FileMode:         0o644,
		MismatchHintTmpl: "noop %s",
		Now:              func() time.Time { return *clock },
	})
	require.NoError(t, err)
	return s
}

func assertTime(t *testing.T, want, got time.Time, msg string) {
	t.Helper()
	assert.True(t, got.Equal(want), "%s: got %v, want %v", msg, got, want)
}

func TestStore_OpenOnMissingFileCreatesEmptyStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	// No file yet on disk, so Snapshot must be empty.
	snap, err := s.Snapshot(t.Context())
	require.NoError(t, err)
	assert.Empty(t, snap)
	// The file only materializes on the first commit.
	_, err = os.Stat(path)
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
	// The first commit triggers the file write.
	require.NoError(t, resolveCommit(t.Context(), s, "worker-1", []byte("x"), []byte("m"), []byte("sl")))

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(), "parent dir mode should match DirMode")

	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm(), "pin file mode should match FileMode")
}

func TestStore_ResolveFirstContactRecordsBase64Keys(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	require.NoError(t, resolveCommit(t.Context(), s, "worker-A", []byte("xkey"), []byte("mkey"), []byte("slkey")))

	snap := snapshot(t, s)
	require.Len(t, snap, 1)
	entry, ok := snap["worker-A"]
	require.True(t, ok)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("xkey")), entry.X25519PublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("mkey")), entry.MlkemPublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("slkey")), entry.SlhdsaPublicKey)
	assert.False(t, entry.FirstSeenAt.IsZero())
	assert.False(t, entry.LastUsedAt.IsZero())
}

func TestStore_ResolveSecondContactWithMatchingKeysSucceedsAndBumpsLastUsedAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	s := openStoreAt(t, path, &clock)

	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	first := snapshot(t, s)["w"]

	// Second contact with identical keys must succeed, preserve
	// FirstSeenAt, and (past the throttle window) bump LastUsedAt.
	clock = start.Add(20 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	second := snapshot(t, s)["w"]
	assertTime(t, first.FirstSeenAt, second.FirstSeenAt, "FirstSeenAt is immutable after first contact")
	assertTime(t, start.Add(20*time.Minute), second.LastUsedAt, "LastUsedAt advances past the throttle window")
}

func TestStore_ResolveMismatchReturnsHintWithWorkerID(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	require.NoError(t, resolveCommit(t.Context(), s, "w-A", []byte("x"), []byte("m"), []byte("sl")))

	// Different X25519 key for the same worker_id triggers mismatch.
	err := resolveCommit(t.Context(), s, "w-A", []byte("x-NEW"), []byte("m"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
	assert.Contains(t, err.Error(), "w-A", "error must surface worker_id so user knows what to clear")
	assert.Contains(t, err.Error(), "test-cli pins remove --worker-id=w-A",
		"error must surface the configured clear-command hint")
	// Both fingerprints, so an operator can compare them with the one the worker
	// logs when it starts.
	assert.Contains(t, err.Error(), "pinned "+noise.CompositeKeyFingerprint([]byte("x"), []byte("m"), []byte("sl")))
	assert.Contains(t, err.Error(), "presented "+noise.CompositeKeyFingerprint([]byte("x-NEW"), []byte("m"), []byte("sl")))
	assert.True(t, strings.HasPrefix(err.Error(), "worker w-A key mismatch"),
		"the error still starts with the worker and the fact: %v", err)
}

// A malformed pin file fails to load. The error includes the presented
// fingerprint and repair guidance without advertising a command that cannot
// read the same file.
func TestStore_ResolveMismatchReportsAnUnreadablePinnedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	document := []byte(`{
  "pins": {
    "w-B": {
      "x25519_public_key": "not!base64",
      "mlkem_public_key": "bWxrZW0=",
      "slhdsa_public_key": "c2xoZHNh",
      "first_seen_at": "2024-01-01T00:00:00Z",
      "last_used_at": "2024-01-01T00:00:00Z"
    }
  }
}`)
	require.NoError(t, os.WriteFile(path, document, 0o600))
	s := openStore(t, path)

	err := resolveCommit(t.Context(), s, "w-B", []byte("x"), []byte("m"), []byte("sl"))
	require.ErrorContains(t, err, "parse pins")
	assert.ErrorContains(t, err, "x25519_public_key")
	assert.ErrorContains(t, err, "Repair the pin file before retrying")
	var cause base64.CorruptInputError
	assert.ErrorAs(t, err, &cause)
	assert.Contains(t, err.Error(), "pinned <unreadable>")
	assert.Contains(t, err.Error(), "presented "+noise.CompositeKeyFingerprint([]byte("x"), []byte("m"), []byte("sl")))
	assert.NotContains(t, err.Error(), "pins remove")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, document, after)
}

func TestStore_ResolveMismatchOnSecondaryKeyTriggersError(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))

	// Only the MLKEM key differs; mismatch must still fire (a partial
	// re-key still indicates the worker's identity has shifted).
	err := resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m-NEW"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
}

func TestStore_ResolveRequiresWorkerID(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	_, err := s.Resolve(t.Context(), "", []byte("x"), []byte("m"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worker_id required")
}

func TestStore_RemoveIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	// Remove on a never-seen worker_id is a no-op (no error, no
	// revision advance).
	require.NoError(t, s.Remove(t.Context(), "never-seen"))
	revision, err := s.RemovalRevision(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(0), revision, "removing an absent pin must not advance the revision")

	// Remove an existing entry, then double-remove.
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, s.Remove(t.Context(), "w"))
	revision, err = s.RemovalRevision(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), revision, "removing an existing pin advances the revision once")
	require.NoError(t, s.Remove(t.Context(), "w"))
	revision, err = s.RemovalRevision(t.Context())
	require.NoError(t, err)
	assert.Equal(t, uint64(1), revision, "a repeated removal is idempotent")
	assert.Empty(t, snapshot(t, s))
}

func TestStore_RemoveAllowsRePinWithDifferentKeys(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))

	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, s.Remove(t.Context(), "w"))

	// After Remove, a TOFU re-pin with different keys must succeed.
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x-NEW"), []byte("m-NEW"), []byte("sl-NEW")))
	entry := snapshot(t, s)["w"]
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x-NEW")), entry.X25519PublicKey)
}

func TestStore_PersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")

	first := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), first, "w-A", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, resolveCommit(t.Context(), first, "w-B", []byte("x2"), []byte("m2"), []byte("sl2")))

	// A second instance must read the same pins from disk.
	second := openStore(t, path)
	snap := snapshot(t, second)
	require.Len(t, snap, 2)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x")), snap["w-A"].X25519PublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x2")), snap["w-B"].X25519PublicKey)

	// A later Resolve against the re-opened store must enforce the
	// recorded keys — proving they came from disk, not from an empty
	// store that would TOFU-record anything.
	err := resolveCommit(t.Context(), second, "w-A", []byte("x-NEW"), []byte("m"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key mismatch")
}

func TestStore_PersistsAsWrapperFormat(t *testing.T) {
	// On-disk shape pins the JSON wrapper format so a future reader
	// (older binary, manual cat) can identify the file. Without this,
	// a refactor that flips to a bare map would silently lose
	// existing pins.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))

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

func TestStore_ResolveCommitIsConcurrencySafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	ctx := t.Context()

	// 50 workers, 10 goroutines per worker hammering Resolve+commit
	// with the same keys; the file lock must serialize them so the
	// first recorded keys win and every call succeeds as a match or a
	// same-key install.
	const workers = 50
	const goroutinesPerWorker = 10
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		id := "w-" + string(rune('A'+(i%26))) + string(rune('A'+(i/26)))
		for j := 0; j < goroutinesPerWorker; j++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if err := resolveCommit(ctx, s, id, []byte("x"), []byte("m"), []byte("sl")); err != nil {
					t.Errorf("resolveCommit(%s): %v", id, err)
				}
			}(id)
		}
	}
	wg.Wait()

	snap := snapshot(t, s)
	require.Len(t, snap, workers, "every worker keeps exactly one pin")
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

func TestStore_SnapshotReturnsACopyNotALiveReference(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, filepath.Join(dir, "pins.json"))
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))

	snap1 := snapshot(t, s)
	require.Contains(t, snap1, "w")
	delete(snap1, "w") // mutate the snapshot

	// The store must NOT be affected by the caller's mutation. Each
	// Snapshot is a copy independent of anything the store holds, so
	// pin enumeration during teardown can't be hijacked by a
	// misbehaving caller.
	snap2 := snapshot(t, s)
	assert.Contains(t, snap2, "w", "Snapshot must return a copy independent of the store's state")
}

func TestStore_CommitPersistsLastUsedAtJustBelowAndAtTenMinutes(t *testing.T) {
	// The throttle measures the ten-minute interval from the persisted
	// last_used_at. Frequent matching calls just below the interval
	// must not rewrite the file; the first call at the interval must
	// persist the bump.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	s := openStoreAt(t, path, &clock)

	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	entry := snapshot(t, s)["w"]
	assertTime(t, start, entry.FirstSeenAt, "first contact records the clock reading")
	assertTime(t, start, entry.LastUsedAt, "first contact records the clock reading")

	for _, elapsed := range []time.Duration{time.Minute, 5 * time.Minute, 10*time.Minute - time.Second} {
		clock = start.Add(elapsed)
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after, "a matching call at +%s must not rewrite the file", elapsed)
	}
	entry = snapshot(t, s)["w"]
	assertTime(t, start, entry.LastUsedAt, "the persisted stamp stays at the last write below the interval")

	clock = start.Add(10 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	entry = snapshot(t, s)["w"]
	assertTime(t, start, entry.FirstSeenAt, "FirstSeenAt never moves")
	assertTime(t, start.Add(10*time.Minute), entry.LastUsedAt, "the call at ten minutes persists the bump")

	for _, elapsed := range []time.Duration{11 * time.Minute, 15 * time.Minute, 20*time.Minute - time.Second} {
		clock = start.Add(elapsed)
		before, err := os.ReadFile(path)
		require.NoError(t, err)
		require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
		after, err := os.ReadFile(path)
		require.NoError(t, err)
		assert.Equal(t, before, after, "a matching call at +%s must not rewrite the file", elapsed)
	}
	entry = snapshot(t, s)["w"]
	assertTime(t, start.Add(10*time.Minute), entry.LastUsedAt, "the interval measures from the persisted stamp")

	clock = start.Add(20 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	entry = snapshot(t, s)["w"]
	assertTime(t, start.Add(20*time.Minute), entry.LastUsedAt, "the call at twenty minutes persists the next bump")
}

func TestStore_AFreshHandleHonorsThePersistedLastUsedAtThrottle(t *testing.T) {
	// A reopen (or a second process) must throttle against the stamp
	// already on disk rather than rewrite every pin on startup.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	first := openStoreAt(t, path, &clock)
	require.NoError(t, resolveCommit(t.Context(), first, "w", []byte("x"), []byte("m"), []byte("sl")))

	second := openStoreAt(t, path, &clock)
	clock = start.Add(5 * time.Minute)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	beforeInfo, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, resolveCommit(t.Context(), second, "w", []byte("x"), []byte("m"), []byte("sl")))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	afterInfo, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a fresh handle inside the throttle window must not rewrite the pin file")
	assert.True(t, os.SameFile(beforeInfo, afterInfo), "the throttled open must not atomically replace identical bytes")

	clock = start.Add(10 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), second, "w", []byte("x"), []byte("m"), []byte("sl")))
	entry := snapshot(t, second)["w"]
	assertTime(t, start.Add(10*time.Minute), entry.LastUsedAt, "the fresh handle flushes at the interval")
}

func TestStore_BackwardClockMovementRepairsLastUsedAt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start.Add(time.Hour)
	first := openStoreAt(t, path, &clock)
	require.NoError(t, resolveCommit(t.Context(), first, "w", []byte("x"), []byte("m"), []byte("sl")))

	clock = start
	second := openStoreAt(t, path, &clock)
	require.NoError(t, resolveCommit(t.Context(), second, "w", []byte("x"), []byte("m"), []byte("sl")))
	assertTime(t, start, snapshot(t, second)["w"].LastUsedAt,
		"a backward clock correction must not leave a future throttle baseline")

	clock = start.Add(5 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), second, "w", []byte("x"), []byte("m"), []byte("sl")))
	assertTime(t, start, snapshot(t, second)["w"].LastUsedAt,
		"the repaired timestamp starts the ordinary throttle interval")

	clock = start.Add(10 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), second, "w", []byte("x"), []byte("m"), []byte("sl")))
	assertTime(t, clock, snapshot(t, second)["w"].LastUsedAt,
		"the repaired throttle persists again at the normal interval")
}

func TestStore_ResolveFlushesLastUsedAtPastThrottleWindow(t *testing.T) {
	// With a custom short throttle the flush happens at the custom
	// interval, measured from the persisted stamp.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	s, err := tofupins.Open(tofupins.Options{
		Path:                  path,
		DirMode:               0o755,
		FileMode:              0o644,
		MismatchHintTmpl:      "noop %s",
		LastUsedPersistMinGap: 5 * time.Minute,
		Now:                   func() time.Time { return clock },
	})
	require.NoError(t, err)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))

	clock = start.Add(5*time.Minute - time.Second)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "just below the custom gap the file must not change")

	clock = start.Add(5 * time.Minute)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	entry := snapshot(t, s)["w"]
	assertTime(t, start.Add(5*time.Minute), entry.LastUsedAt, "at the custom gap the bump is persisted")
}

func TestStore_ResolveFirstContactAlwaysWritesEvenWithThrottle(t *testing.T) {
	// First contact (no pre-existing pin) creates a new Entry and must
	// always persist regardless of the throttle — there's no
	// previously-persisted value to compare against.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	s, err := tofupins.Open(tofupins.Options{
		Path:                  path,
		DirMode:               0o755,
		FileMode:              0o644,
		MismatchHintTmpl:      "noop %s",
		LastUsedPersistMinGap: 24 * time.Hour, // very aggressive throttle
		Now:                   func() time.Time { return clock },
	})
	require.NoError(t, err)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))

	// File must exist after first contact even under heavy throttle.
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))
}

func TestStore_CommitRefusesWhenTheMatchingPinIsRemovedDuringTheHeldOpen(t *testing.T) {
	// Resolve a matching pin inside the throttle, then remove it during
	// the held open. The commit must refuse the channel instead of
	// publishing a stale decision.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	s := openStoreAt(t, path, &clock)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))

	clock = start.Add(time.Minute) // inside the throttle window
	commit, err := s.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err, "the resolve step sees the matching pin")

	require.NoError(t, s.Remove(t.Context(), "w"))

	err = commit(t.Context())
	require.Error(t, err, "the commit must refuse the channel after the removal")
	assert.Contains(t, err.Error(), "removal revision changed")
	assert.Empty(t, snapshot(t, s), "the refused commit must not restore the removed pin")
}

func TestStore_TwoHandlesDetectARemovalInsteadOfRestoringThePin(t *testing.T) {
	// One handle holds a transaction open while a second handle removes
	// the pin. The stale commit must detect the removal, not restore
	// the pin from its captured state.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	first := openStore(t, path)
	second := openStore(t, path)

	require.NoError(t, resolveCommit(t.Context(), first, "w", []byte("x"), []byte("m"), []byte("sl")))
	commit, err := first.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)

	require.NoError(t, second.Remove(t.Context(), "w"))

	err = commit(t.Context())
	require.Error(t, err, "the stale commit must detect the removal")
	assert.Contains(t, err.Error(), "removal revision changed")
	assert.NotContains(t, snapshot(t, first), "w", "the new store must not restore a removed pin")
	assert.NotContains(t, snapshot(t, second), "w", "the second handle sees the same state")

	// A fresh Resolve observes the removal and records a replacement
	// pin through the normal TOFU path.
	require.NoError(t, resolveCommit(t.Context(), first, "w", []byte("x-NEW"), []byte("m-NEW"), []byte("sl-NEW")))
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("x-NEW")), snapshot(t, first)["w"].X25519PublicKey)
}

func TestStore_SecondFirstContactCommitWithTheSameKeysUsesThePersistedThrottle(t *testing.T) {
	// Concurrent first contacts with identical keys use the same persisted
	// throttle as a match observed during Resolve.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clock := start
	first := openStoreAt(t, path, &clock)
	second := openStoreAt(t, path, &clock)

	commit1, err := first.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)
	commit2, err := second.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)

	commitAtInterval, err := second.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)

	require.NoError(t, commit1(t.Context()))
	clock = start.Add(3 * time.Minute)
	require.NoError(t, commit2(t.Context()))

	entry := snapshot(t, first)["w"]
	assertTime(t, start, entry.FirstSeenAt, "the same-key commit preserves the first install's first_seen_at")
	assertTime(t, start, entry.LastUsedAt, "the persisted timestamp does not change within the throttle interval")

	clock = start.Add(10 * time.Minute)
	require.NoError(t, commitAtInterval(t.Context()))
	entry = snapshot(t, first)["w"]
	assertTime(t, start, entry.FirstSeenAt, "a timestamp update preserves the first install's first_seen_at")
	assertTime(t, clock, entry.LastUsedAt, "the persisted timestamp changes at the throttle interval")
}

func TestStore_SecondFirstContactCommitWithDifferentKeysIsRefused(t *testing.T) {
	// Two resolves both observe first contact with different keys.
	// Whoever commits second must be refused: the first install wins.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	first := openStore(t, path)
	second := openStore(t, path)

	commit1, err := first.Resolve(t.Context(), "w", []byte("x-1"), []byte("m-1"), []byte("sl-1"))
	require.NoError(t, err)

	// A competing first contact installs different keys in between.
	require.NoError(t, resolveCommit(t.Context(), second, "w", []byte("x-2"), []byte("m-2"), []byte("sl-2")))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	err = commit1(t.Context())
	require.Error(t, err, "a different-key install during the held open must refuse the commit")
	assert.Contains(t, err.Error(), "installed with different keys")
	assert.Contains(t, err.Error(), "pinned "+noise.CompositeKeyFingerprint([]byte("x-2"), []byte("m-2"), []byte("sl-2")))
	assert.Contains(t, err.Error(), "presented "+noise.CompositeKeyFingerprint([]byte("x-1"), []byte("m-1"), []byte("sl-1")))
	assert.Contains(t, err.Error(), "test-cli pins remove --worker-id=w")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "the installed pin survives the refused commit")
}

func TestStore_CommitDetectsAPinThatDisappearedDuringTheHeldOpen(t *testing.T) {
	// A hand-edit removes the entry without advancing the removal
	// revision. The commit must still detect the disappearance.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	commit, err := s.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(path, []byte(`{"pins":{}}`), 0o644))

	err = commit(t.Context())
	require.Error(t, err, "the commit must detect that the observed pin disappeared")
	assert.Contains(t, err.Error(), "disappeared during open")
	assert.NotContains(t, snapshot(t, s), "w", "a refused commit must not write a pin")
}

func TestStore_CommitDetectsAPinThatChangedDuringTheHeldOpen(t *testing.T) {
	// A hand-edit replaces the entry's keys without advancing the
	// removal revision. The commit must detect the replacement.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")))
	commit, err := s.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)

	document := []byte(`{
  "pins": {
    "w": {
      "x25519_public_key": "eC1ORVc=",
      "mlkem_public_key": "bS1ORVc=",
      "slhdsa_public_key": "c2wtTkVX",
      "first_seen_at": "2026-11-20T00:00:00Z",
      "last_used_at": "2026-11-20T00:00:00Z"
    }
  }
}`)
	require.NoError(t, os.WriteFile(path, document, 0o644))

	err = commit(t.Context())
	require.Error(t, err, "the commit must detect that the observed pin changed")
	assert.Contains(t, err.Error(), "changed during open")
	assert.Contains(t, err.Error(), "pinned "+noise.CompositeKeyFingerprint([]byte("x-NEW"), []byte("m-NEW"), []byte("sl-NEW")))
	assert.Contains(t, err.Error(), "presented "+noise.CompositeKeyFingerprint([]byte("x"), []byte("m"), []byte("sl")))
	assert.Contains(t, err.Error(), "test-cli pins remove --worker-id=w")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, document, after, "the replacement pin survives the refused commit")
}

func TestStore_OperationsFailOnAMalformedDocumentUntilItIsRepaired(t *testing.T) {
	// A corrupt write or hand-edit must fail every operation without
	// dropping the file, and a repaired document must work again.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	require.NoError(t, os.WriteFile(path, []byte("{not valid json"), 0o644))

	// Open prepares the path; the document is only read per operation.
	s := openStore(t, path)

	_, err := s.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pins")
	_, err = s.Snapshot(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse pins")
	require.Error(t, s.Remove(t.Context(), "w"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "{not valid json", string(data), "a failed read must leave the file untouched")

	require.NoError(t, os.WriteFile(path, []byte(`{"pins":{}}`), 0o644))
	require.NoError(t, resolveCommit(t.Context(), s, "w", []byte("x"), []byte("m"), []byte("sl")),
		"a repaired document is usable by a later open")
	assert.Contains(t, snapshot(t, s), "w")
}

func TestStore_OperationsFailOnANullOrAbsentPinsObject(t *testing.T) {
	// An existing file requires a nonnull `pins` object; `null` and a
	// missing key are both refusals, not confirmed empty state.
	for _, document := range []string{`{"pins":null}`, `{}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "pins.json")
		require.NoError(t, os.WriteFile(path, []byte(document), 0o644))
		s := openStore(t, path)

		_, err := s.Resolve(t.Context(), "w", []byte("x"), []byte("m"), []byte("sl"))
		require.Error(t, err, "document %s", document)
		assert.Contains(t, err.Error(), `missing or null "pins"`, "document %s", document)
		_, err = s.Snapshot(t.Context())
		require.Error(t, err, "document %s", document)
		require.Error(t, s.Remove(t.Context(), "w"), "document %s", document)
	}
}

func TestStore_OperationsRejectDuplicateWorkerPins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	store := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), store, "worker-a", []byte("x"), []byte("m"), []byte("sl")))
	pins, err := store.Snapshot(t.Context())
	require.NoError(t, err)
	validPin, err := json.Marshal(pins["worker-a"])
	require.NoError(t, err)
	pending, err := store.Resolve(t.Context(), "worker-b", []byte("x-b"), []byte("m-b"), []byte("sl-b"))
	require.NoError(t, err)
	document := []byte(fmt.Sprintf(`{"pins":{"worker-a":null,"worker-a":%s}}`, validPin))
	require.NoError(t, os.WriteFile(path, document, 0o644))

	_, err = store.Resolve(t.Context(), "worker-a", []byte("x"), []byte("m"), []byte("sl"))
	assert.ErrorContains(t, err, "duplicate pin")
	_, err = store.Snapshot(t.Context())
	assert.ErrorContains(t, err, "duplicate pin")
	assert.ErrorContains(t, store.Remove(t.Context(), "worker-a"), "duplicate pin")
	assert.ErrorContains(t, pending(t.Context()), "duplicate pin")
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, document, after)
}

func TestStore_OperationsRejectNullRevisionAndEntries(t *testing.T) {
	for _, test := range []struct {
		name, document, invalidField string
	}{
		{"revision", `{"pins":{},"removal_revision":null}`, "removal_revision"},
		{"entry", `{"pins":{"worker-a":null},"removal_revision":0}`, "worker-a"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pins.json")
			store := openStore(t, path)
			commit, err := store.Resolve(t.Context(), "worker-b", []byte("x"), []byte("m"), []byte("sl"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, []byte(test.document), 0o644))

			_, err = store.Resolve(t.Context(), "worker-b", []byte("x"), []byte("m"), []byte("sl"))
			assert.ErrorContains(t, err, test.invalidField)
			_, err = store.Snapshot(t.Context())
			assert.ErrorContains(t, err, test.invalidField)
			assert.ErrorContains(t, store.Remove(t.Context(), "worker-a"), test.invalidField)
			assert.ErrorContains(t, commit(t.Context()), test.invalidField)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, test.document, string(after))
		})
	}
}

func TestStore_OperationsRejectMalformedFieldsInAnUnrelatedPin(t *testing.T) {
	for _, test := range []struct {
		desc  string
		field string
		value json.RawMessage
	}{
		{desc: "missing x25519 key", field: "x25519_public_key"},
		{desc: "missing ML-KEM key", field: "mlkem_public_key"},
		{desc: "missing SLH-DSA key", field: "slhdsa_public_key"},
		{desc: "null x25519 key", field: "x25519_public_key", value: json.RawMessage(`null`)},
		{desc: "null ML-KEM key", field: "mlkem_public_key", value: json.RawMessage(`null`)},
		{desc: "null SLH-DSA key", field: "slhdsa_public_key", value: json.RawMessage(`null`)},
		{desc: "undecodable key", field: "mlkem_public_key", value: json.RawMessage(`"not!base64"`)},
		{desc: "key with nonzero padding bits", field: "slhdsa_public_key", value: json.RawMessage(`"Ax=="`)},
		{desc: "key with a line break", field: "x25519_public_key", value: json.RawMessage(`"eA==\n"`)},
		{desc: "numeric key", field: "mlkem_public_key", value: json.RawMessage(`3`)},
		{desc: "missing first-seen timestamp", field: "first_seen_at"},
		{desc: "missing last-used timestamp", field: "last_used_at"},
		{desc: "null first-seen timestamp", field: "first_seen_at", value: json.RawMessage(`null`)},
		{desc: "null last-used timestamp", field: "last_used_at", value: json.RawMessage(`null`)},
		{desc: "invalid timestamp", field: "first_seen_at", value: json.RawMessage(`"2026-11-20"`)},
		{desc: "numeric timestamp", field: "last_used_at", value: json.RawMessage(`3`)},
		{desc: "single-digit timestamp hour", field: "first_seen_at", value: json.RawMessage(`"2026-11-20T1:00:00Z"`)},
		{desc: "comma before fractional seconds", field: "last_used_at", value: json.RawMessage(`"2026-11-20T01:00:00,1Z"`)},
		{desc: "timezone hour out of range", field: "last_used_at", value: json.RawMessage(`"2026-11-20T01:00:00+24:00"`)},
	} {
		t.Run(test.desc, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pins.json")
			store := openStore(t, path)
			require.NoError(t, resolveCommit(t.Context(), store, "worker-a", []byte("x"), []byte("m"), []byte("sl")))
			commit, err := store.Resolve(t.Context(), "worker-new", []byte("x-new"), []byte("m-new"), []byte("sl-new"))
			require.NoError(t, err)

			pins, err := store.Snapshot(t.Context())
			require.NoError(t, err)
			otherPin, err := json.Marshal(pins["worker-a"])
			require.NoError(t, err)
			var malformed map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(otherPin, &malformed))
			if test.value == nil {
				delete(malformed, test.field)
			} else {
				malformed[test.field] = test.value
			}
			invalidPin, err := json.Marshal(malformed)
			require.NoError(t, err)
			document := fmt.Appendf(nil, `{"pins":{"worker-a":%s,"worker-invalid":%s}}`, otherPin, invalidPin)
			require.NoError(t, os.WriteFile(path, document, 0o644))

			_, resolveErr := store.Resolve(t.Context(), "worker-a", []byte("x"), []byte("m"), []byte("sl"))
			_, snapshotErr := store.Snapshot(t.Context())
			removeErr := store.Remove(t.Context(), "worker-a")
			commitErr := commit(t.Context())
			for _, operation := range []struct {
				name string
				err  error
			}{
				{"resolve", resolveErr},
				{"snapshot", snapshotErr},
				{"remove", removeErr},
				{"commit", commitErr},
			} {
				assert.ErrorContains(t, operation.err, "parse pins", operation.name)
				assert.ErrorContains(t, operation.err, "worker-invalid", operation.name)
				assert.ErrorContains(t, operation.err, test.field, operation.name)
			}

			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, document, after)
		})
	}
}

func TestStore_SnapshotReadsRequiredRFC3339Timestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	document := []byte(`{"pins":{"w":{
		"x25519_public_key":"eA==",
		"mlkem_public_key":"bQ==",
		"slhdsa_public_key":"c2w=",
		"first_seen_at":"0001-01-01t00:00:00z",
		"last_used_at":"2026-11-20T01:02:03.456789+09:00"
	}}}`)
	require.NoError(t, os.WriteFile(path, document, 0o644))
	pins := snapshot(t, openStore(t, path))
	require.Contains(t, pins, "w")
	entry := pins["w"]

	assert.True(t, entry.FirstSeenAt.IsZero())
	assertTime(t, time.Date(2026, 11, 19, 16, 2, 3, 456789000, time.UTC), entry.LastUsedAt,
		"timestamp preserves fractional seconds and applies the timezone offset")
}

func TestStore_RemoveRejectsARevisionOverflowBeforeChangingTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	document := fmt.Sprintf(`{
  "pins": {
    "w": {
      "x25519_public_key": "eA==",
      "mlkem_public_key": "bQ==",
      "slhdsa_public_key": "c2w=",
      "first_seen_at": "2026-11-20T00:00:00Z",
      "last_used_at": "2026-11-20T00:00:00Z"
    }
  },
  "removal_revision": %d
}`, uint64(math.MaxUint64))
	require.NoError(t, os.WriteFile(path, []byte(document), 0o644))
	s := openStore(t, path)

	err := s.Remove(t.Context(), "w")
	require.Error(t, err, "an overflowing revision must be rejected")
	assert.Contains(t, err.Error(), "removal revision overflow")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, document, string(after), "overflow must be rejected before changing the file")
	assert.Contains(t, snapshot(t, s), "w")
}

func TestStore_CommitRefusesWhenAnUnrelatedRemovalAdvancesTheRevision(t *testing.T) {
	// A removal of any pin invalidates a pending open for every other
	// worker sharing the file.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), s, "w-A", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, resolveCommit(t.Context(), s, "w-B", []byte("x2"), []byte("m2"), []byte("sl2")))
	beforeA := snapshot(t, s)["w-A"]

	commit, err := s.Resolve(t.Context(), "w-A", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)

	require.NoError(t, s.Remove(t.Context(), "w-B"))

	err = commit(t.Context())
	require.Error(t, err, "an unrelated removal must invalidate the pending open")
	assert.Contains(t, err.Error(), "removal revision changed")

	snap := snapshot(t, s)
	assert.NotContains(t, snap, "w-B", "the removal stands")
	require.Contains(t, snap, "w-A", "the refused commit leaves the unrelated pin untouched")
	assertTime(t, beforeA.FirstSeenAt, snap["w-A"].FirstSeenAt, "the unrelated pin keeps its stamps")
	assertTime(t, beforeA.LastUsedAt, snap["w-A"].LastUsedAt, "the unrelated pin keeps its stamps")
}

func TestStore_ACanceledCommitLeavesTheFileUnchanged(t *testing.T) {
	// Cancellation that wins before the write starts prevents the
	// commit entirely.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	commit, err := s.Resolve(ctx, "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)
	cancel()

	require.ErrorIs(t, commit(ctx), context.Canceled)
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "a canceled commit must not create the pin file")
	assert.Empty(t, snapshot(t, s))
}

func TestStore_ACancellationBeforeTheAtomicWriteLeavesTheFileUnchanged(t *testing.T) {
	// The commit checks the context after lock acquisition and again
	// immediately before the atomic write. A cancellation that wins at
	// that last point still prevents the publication.
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	clockReads := 0
	s, err := tofupins.Open(tofupins.Options{
		Path:             path,
		DirMode:          0o755,
		FileMode:         0o644,
		MismatchHintTmpl: "noop %s",
		Now: func() time.Time {
			clockReads++
			cancel() // cancel at the last point before the replacement
			return start
		},
	})
	require.NoError(t, err)

	commit, err := s.Resolve(ctx, "w", []byte("x"), []byte("m"), []byte("sl"))
	require.NoError(t, err)
	require.Zero(t, clockReads, "Resolve must not read the clock")

	require.ErrorIs(t, commit(ctx), context.Canceled)
	assert.Equal(t, 1, clockReads, "commit reads the clock only in the pre-write window")
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr), "cancellation before the atomic write must prevent the commit")
}

func TestStore_AFailingReplacementLeavesThePreviousStateIntact(t *testing.T) {
	// A failed write must leave the previous trust decision on disk.
	if runtime.GOOS == "windows" {
		t.Skip("directory mode bits aren't meaningful on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("a root process ignores directory mode bits")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pins.json")
	s := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), s, "w-A", []byte("x"), []byte("m"), []byte("sl")))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	// A read-only parent directory makes the atomic replacement fail
	// at its temporary file.
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	commit, err := s.Resolve(t.Context(), "w-B", []byte("x2"), []byte("m2"), []byte("sl2"))
	require.NoError(t, err, "the decision step reads the intact file")
	require.Error(t, commit(t.Context()), "the replacement cannot publish into a read-only directory")

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after, "a failed replacement must leave the previous trust decision intact")
	assert.NotContains(t, snapshot(t, s), "w-B")
}

// helperEnv variables parameterize the helper process the cross-process
// test re-executes the test binary as.
const (
	helperModeEnv   = "LEAPMUX_TOFUPINS_HELPER_MODE"
	helperPathEnv   = "LEAPMUX_TOFUPINS_HELPER_PATH"
	helperWorkerEnv = "LEAPMUX_TOFUPINS_HELPER_WORKER"
	helperXEnv      = "LEAPMUX_TOFUPINS_HELPER_X"
	helperMEnv      = "LEAPMUX_TOFUPINS_HELPER_M"
	helperSLEnv     = "LEAPMUX_TOFUPINS_HELPER_SL"
	helperRemoveEnv = "LEAPMUX_TOFUPINS_HELPER_REMOVE_ID"
	helperPinEnv    = "LEAPMUX_TOFUPINS_HELPER_PIN_ID"
)

// transactionWaitContext reports when the store blocks on the native lock.
type transactionWaitContext struct {
	context.Context
	once sync.Once
}

func (c *transactionWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { fmt.Println("waiting") })
	return c.Context.Done()
}

// TestHelperProcessTofupinsStore exercises the store in a separate process.
// The parent drives the holder and canceled waiter through standard input.
func TestHelperProcessTofupinsStore(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	options := tofupins.Options{
		Path:             os.Getenv(helperPathEnv),
		DirMode:          0o755,
		FileMode:         0o644,
		MismatchHintTmpl: "noop %s",
	}
	if mode == "hold" {
		options.Now = func() time.Time {
			fmt.Println("read")
			scanner := bufio.NewScanner(os.Stdin)
			require.True(t, scanner.Scan(), "the parent releases the transaction")
			require.Equal(t, "release", scanner.Text())
			return time.Now()
		}
	}
	store, err := tofupins.Open(options)
	require.NoError(t, err)
	x := []byte(os.Getenv(helperXEnv))
	m := []byte(os.Getenv(helperMEnv))
	sl := []byte(os.Getenv(helperSLEnv))

	switch mode {
	case "hold":
		require.NoError(t, resolveCommit(t.Context(), store, os.Getenv(helperWorkerEnv), x, m, sl))
		fmt.Println("committed")
	case "remove-and-pin":
		ctx := &transactionWaitContext{Context: t.Context()}
		require.NoError(t, store.Remove(ctx, os.Getenv(helperRemoveEnv)))
		require.NoError(t, resolveCommit(t.Context(), store, os.Getenv(helperPinEnv), x, m, sl))
		fmt.Println("committed")
	case "canceled-waiter":
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			done <- store.Remove(&transactionWaitContext{Context: ctx}, os.Getenv(helperWorkerEnv))
		}()
		scanner := bufio.NewScanner(os.Stdin)
		require.True(t, scanner.Scan(), "the parent cancels the waiter")
		require.Equal(t, "cancel", scanner.Text())
		cancel()
		require.ErrorIs(t, <-done, context.Canceled)
		fmt.Println("canceled")
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func startStoreHelper(t *testing.T, ctx context.Context, env ...string) (io.WriteCloser, *bufio.Reader, func() error) {
	t.Helper()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcessTofupinsStore$")
	command.Env = append(os.Environ(), env...)
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	command.Stderr = os.Stderr
	require.NoError(t, command.Start())
	var waitOnce sync.Once
	var waitErr error
	wait := func() error {
		waitOnce.Do(func() { waitErr = command.Wait() })
		return waitErr
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = wait()
	})
	return stdin, bufio.NewReader(stdout), wait
}

func readLine(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	line, err := r.ReadString('\n')
	require.NoError(t, err)
	return strings.TrimSuffix(line, "\n")
}

func writeLine(w io.Writer, line string) error {
	_, err := io.WriteString(w, line+"\n")
	return err
}

func TestStore_HeldTransactionAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pins.json")
	store := openStore(t, path)
	require.NoError(t, resolveCommit(t.Context(), store, "worker-to-remove", []byte("x"), []byte("m"), []byte("sl")))
	require.NoError(t, resolveCommit(t.Context(), store, "worker-to-keep", []byte("xk"), []byte("mk"), []byte("slk")))
	keptPin := snapshot(t, store)["worker-to-keep"]
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	holderInput, holderOutput, waitHolder := startStoreHelper(t, ctx,
		helperModeEnv+"=hold",
		helperPathEnv+"="+path,
		helperWorkerEnv+"=worker-first",
		helperXEnv+"=xf", helperMEnv+"=mf", helperSLEnv+"=slf",
	)
	require.Equal(t, "read", readLine(t, holderOutput), "the holder has read the file under the lock")

	_, writerOutput, waitWriter := startStoreHelper(t, ctx,
		helperModeEnv+"=remove-and-pin",
		helperPathEnv+"="+path,
		helperRemoveEnv+"=worker-to-remove",
		helperPinEnv+"=worker-second",
		helperXEnv+"=xs", helperMEnv+"=ms", helperSLEnv+"=sls",
	)
	require.Equal(t, "waiting", readLine(t, writerOutput), "the second writer contends with the holder")

	waiterInput, waiterOutput, waitWaiter := startStoreHelper(t, ctx,
		helperModeEnv+"=canceled-waiter",
		helperPathEnv+"="+path,
		helperWorkerEnv+"=worker-to-keep",
	)
	require.Equal(t, "waiting", readLine(t, waiterOutput), "the canceled writer also contends with the holder")
	require.NoError(t, writeLine(waiterInput, "cancel"))
	require.Equal(t, "canceled", readLine(t, waiterOutput))
	require.NoError(t, waitWaiter())

	afterCancellation, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, afterCancellation, "the canceled waiter cannot modify the file")

	require.NoError(t, writeLine(holderInput, "release"))
	require.Equal(t, "committed", readLine(t, holderOutput))
	require.NoError(t, waitHolder())
	require.Equal(t, "committed", readLine(t, writerOutput))
	require.NoError(t, waitWriter())

	pins := snapshot(t, openStore(t, path))
	assert.Len(t, pins, 3)
	assert.Equal(t, keptPin, pins["worker-to-keep"])
	assert.NotContains(t, pins, "worker-to-remove")
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("xf")), pins["worker-first"].X25519PublicKey)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("xs")), pins["worker-second"].X25519PublicKey)
	revision, err := store.RemovalRevision(t.Context())
	require.NoError(t, err)
	assert.EqualValues(t, 1, revision)
}
