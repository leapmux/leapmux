package main

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/useronly"
)

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func cookieFor(name, value string) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/"}
}

// privateDir returns a t.TempDir with mode 0700, which MkdirAllPrivate
// requires. t.TempDir on macOS creates 0755.
func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o700))
	return dir
}

func TestSessionStoreSetCookiePersists(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)

	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc123")})

	path := filepath.Join(dir, sessionFileName(base))
	require.FileExists(t, path)
	require.NoError(t, useronly.VerifyFile(path))

	j2 := openPersistentJar(base, dir)
	cookies := j2.Cookies(base)
	require.Len(t, cookies, 1)
	assert.Equal(t, "sid", cookies[0].Name)
	assert.Equal(t, "abc123", cookies[0].Value)
}

func TestSessionStoreMaxAgeZeroRemoves(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)

	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc123")})
	path := filepath.Join(dir, sessionFileName(base))
	require.FileExists(t, path)

	j.SetCookies(base, []*http.Cookie{{Name: "sid", Value: "", Path: "/", MaxAge: -1}})
	assert.NoFileExists(t, path, "the file must be gone after the Hub clears the cookie")
}

// A cleared record removes the session file. A later re-add must write the
// file again even when its bytes match the last write, because the removal
// left no file behind.
func TestSessionStoreReAddAfterRemovalWritesTheFile(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)
	path := filepath.Join(dir, sessionFileName(base))

	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc123")})
	require.FileExists(t, path)

	// Sign-out clears the cookie: the record empties and the file goes.
	j.SetCookies(base, []*http.Cookie{{Name: "sid", Value: "", Path: "/", MaxAge: -1}})
	require.NoFileExists(t, path)

	// The Hub re-issues the identical cookie. Its bytes match the last
	// write, and the re-add must still write the file.
	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc123")})
	require.FileExists(t, path, "the re-add must write the file again")

	j2 := openPersistentJar(base, dir)
	cookies := j2.Cookies(base)
	require.Len(t, cookies, 1)
	assert.Equal(t, "abc123", cookies[0].Value, "a restart finds the re-added session")
}

// The record keeps a clock-expired cookie until a Set-Cookie touches it, while
// the inner jar drops it at read time. Before every persist the record must
// prune what the jar would not return, so the file never holds a credential
// that is already dead.
func TestSessionStoreClockExpiredCookiePrunedBeforePersist(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)
	path := filepath.Join(dir, sessionFileName(base))

	future := time.Now().Add(time.Hour).UTC()
	j.SetCookies(base, []*http.Cookie{{Name: "doomed", Value: "x", Path: "/", Expires: future}})
	j.SetCookies(base, []*http.Cookie{cookieFor("live", "y")})
	require.FileExists(t, path)

	// Simulate the clock passing the cookie expiry: the record keeps the row
	// with an expiry now in the past, exactly as a clock-passed cookie leaves it.
	k := cookieKey(base, &http.Cookie{Name: "doomed", Path: "/"})
	pc := j.record[k]
	past := time.Now().Add(-time.Hour).UTC()
	pc.Expires = &past
	j.record[k] = pc

	j.SetCookies(base, []*http.Cookie{cookieFor("third", "z")})

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf sessionFile
	require.NoError(t, json.Unmarshal(data, &sf))
	names := map[string]bool{}
	for _, c := range sf.Cookies {
		names[c.Name] = true
	}
	assert.False(t, names["doomed"], "clock-expired cookie must not be persisted")
	assert.True(t, names["live"])
	assert.True(t, names["third"])
}

func TestSessionStoreReplaysEffectiveCookieScopes(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example.test")
	jar := openPersistentJar(base, dir)
	jar.SetCookies(mustParse(t, "https://hub.example.test/oauth/token"), []*http.Cookie{
		{Name: "default-path", Value: "step", Secure: true},
		{Name: "host-only", Value: "own", Path: "/"},
		{Name: "domain", Value: "shared", Path: "/", Domain: ".example.test"},
	})
	jar.SetCookies(base, []*http.Cookie{
		{Name: "domain", Path: "/", Domain: "example.test.", MaxAge: -1},
	})

	for label, view := range map[string]http.CookieJar{
		"live": jar, "exchange snapshot": jar.snapshot(), "reopened": openPersistentJar(base, dir),
	} {
		t.Run(label, func(t *testing.T) {
			values := func(address string) map[string]string {
				found := make(map[string]string)
				for _, cookie := range view.Cookies(mustParse(t, address)) {
					found[cookie.Name] = cookie.Value
				}
				return found
			}
			assert.Equal(t, map[string]string{"default-path": "step", "host-only": "own", "domain": "shared"},
				values("https://hub.example.test/oauth/confirm"))
			assert.Equal(t, map[string]string{"host-only": "own", "domain": "shared"},
				values("https://hub.example.test/workspace"))
			assert.Equal(t, map[string]string{"domain": "shared"},
				values("https://another.example.test/oauth/confirm"))
		})
	}
}

func TestSessionStoreExpiredCookieIgnored(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	path := filepath.Join(dir, sessionFileName(base))

	expired := time.Now().Add(-time.Hour).UTC()
	sf := sessionFile{
		HubURL: base.String(),
		Cookies: []persistedCookie{
			{Name: "old", Value: "stale", Path: "/", Expires: &expired},
		},
	}
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, atomicfile.WriteFile(path, data, 0o600))

	j := openPersistentJar(base, dir)
	assert.Empty(t, j.Cookies(base), "an expired cookie must not be sent")
}

func TestSessionStoreWrongOriginIgnored(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	path := filepath.Join(dir, sessionFileName(base))

	sf := sessionFile{
		HubURL:  "http://hub.example",
		Cookies: []persistedCookie{{Name: "sid", Value: "abc", Path: "/"}},
	}
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, atomicfile.WriteFile(path, data, 0o600))

	j := openPersistentJar(base, dir)
	assert.Empty(t, j.Cookies(base), "a file for another origin must be ignored")
}

func TestSessionStoreUnparseableFileIgnored(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	path := filepath.Join(dir, sessionFileName(base))

	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	j := openPersistentJar(base, dir)
	assert.Empty(t, j.Cookies(base), "a corrupt file must leave the jar empty")

	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc")})
	assert.Len(t, j.Cookies(base), 1, "the jar must still work after a corrupt file")
}

func TestSessionStoreWrongPermissionsIgnored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions are a Unix concept")
	}
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	path := filepath.Join(dir, sessionFileName(base))

	sf := sessionFile{
		HubURL:  base.String(),
		Cookies: []persistedCookie{{Name: "sid", Value: "abc", Path: "/"}},
	}
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(path, data, 0o644))

	j := openPersistentJar(base, dir)
	assert.Empty(t, j.Cookies(base), "a file another account can read must be ignored")
}

func TestSessionStoreLeftoverTempFileCleaned(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	path := filepath.Join(dir, sessionFileName(base))
	tmpPath := path + ".tmp12345"

	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(tmpPath, []byte("leftover"), 0o600))

	openPersistentJar(base, dir)
	assert.NoFileExists(t, tmpPath, "leftover temp file must be cleaned on open")

	j := openPersistentJar(base, dir)
	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc")})
	require.FileExists(t, path)
	require.NoError(t, os.WriteFile(tmpPath, []byte("leftover"), 0o600))
	require.FileExists(t, tmpPath)

	j.SetCookies(base, []*http.Cookie{{Name: "sid", Value: "", Path: "/", MaxAge: -1}})
	assert.NoFileExists(t, tmpPath, "leftover temp file must be cleaned on removal")
}

func TestSessionStoreNoDirectory(t *testing.T) {
	workDir := t.TempDir()
	t.Chdir(workDir)
	base := mustParse(t, "https://hub.example")

	j := openPersistentJar(base, "")
	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc123")})

	assert.Len(t, j.Cookies(base), 1, "the jar must keep cookies in memory")

	entries, err := os.ReadDir(workDir)
	require.NoError(t, err)
	assert.Empty(t, entries, "no directory must mean no file written")
}

func TestSessionStoreCaseInsensitiveHost(t *testing.T) {
	dir := privateDir(t)
	upper := mustParse(t, "https://Hub.example")
	lower := mustParse(t, "https://hub.example")

	j := openPersistentJar(upper, dir)
	j.SetCookies(upper, []*http.Cookie{cookieFor("sid", "abc")})

	assert.Equal(t, sessionFileName(upper), sessionFileName(lower),
		"the file name must ignore host case")

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	assert.Len(t, files, 1, "one file for hosts that differ only in case")
}

func TestSessionStoreUsesFixedSizeOriginDigestNames(t *testing.T) {
	dir := privateDir(t)
	hubs := []string{
		"https://bücher.example",
		"https://café.example",
		"http://[::1]",
	}
	seen := make(map[string]bool)
	for i, raw := range hubs {
		base := mustParse(t, raw)
		jar := openPersistentJar(base, dir)
		jar.SetCookies(base, []*http.Cookie{cookieFor("sid", string(rune('a'+i)))})
		name := sessionFileName(base)
		assert.Len(t, name, len("session_")+sha256.Size*2+len(".json"))
		assert.False(t, seen[name], "different origins require different session files")
		seen[name] = true
		require.FileExists(t, filepath.Join(dir, name))
	}
	for i, raw := range hubs {
		jar := openPersistentJar(mustParse(t, raw), dir)
		data, err := useronly.ReadFile(jar.path)
		require.NoError(t, err)
		var sf sessionFile
		require.NoError(t, json.Unmarshal(data, &sf))
		assert.Equal(t, mustParse(t, raw).String(), sf.HubURL)
		require.Len(t, sf.Cookies, 1)
		assert.Equal(t, string(rune('a'+i)), sf.Cookies[0].Value)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	assert.Len(t, files, len(hubs))
}

type observingCookieJar struct {
	http.CookieJar
	afterSet func()
}

func (j observingCookieJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.CookieJar.SetCookies(u, cookies)
	j.afterSet()
}

func TestSessionStoreSerializesLiveAndDiskUpdates(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)
	j.inner = observingCookieJar{
		CookieJar: j.inner,
		afterSet: func() {
			if j.mu.TryLock() {
				j.mu.Unlock()
				t.Error("the live jar update ran outside the persistence lock")
			}
		},
	}
	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "first")})
	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "second")})

	live := j.Cookies(base)
	reopened := openPersistentJar(base, dir).Cookies(base)
	require.Len(t, live, 1)
	require.Len(t, reopened, 1)
	assert.Equal(t, "second", live[0].Value)
	assert.Equal(t, live, reopened, "a restart reads the same cookie the live jar sends")
}

func TestSessionStorePositiveMaxAgeSetsTheStoredExpiry(t *testing.T) {
	const maxAge = 3600
	for _, name := range []string{"no Expires", "past Expires", "future Expires"} {
		t.Run(name, func(t *testing.T) {
			dir := privateDir(t)
			base := mustParse(t, "https://hub.example")
			j := openPersistentJar(base, dir)
			cookie := cookieFor("sid", "fresh")
			cookie.MaxAge = maxAge
			now := time.Now()
			switch name {
			case "past Expires":
				cookie.Expires = now.Add(-time.Hour)
			case "future Expires":
				cookie.Expires = now.Add(24 * time.Hour)
			}
			j.SetCookies(base, []*http.Cookie{cookie})

			data, err := useronly.ReadFile(j.path)
			require.NoError(t, err)
			var sf sessionFile
			require.NoError(t, json.Unmarshal(data, &sf))
			require.Len(t, sf.Cookies, 1)
			require.NotNil(t, sf.Cookies[0].Expires)
			lifetime := maxAge * time.Second
			assert.False(t, sf.Cookies[0].Expires.Before(now.Add(lifetime)))
			assert.False(t, sf.Cookies[0].Expires.After(time.Now().Add(lifetime)))
			reopened := openPersistentJar(base, dir).Cookies(base)
			require.Len(t, reopened, 1)
			assert.Equal(t, "fresh", reopened[0].Value)
		})
	}
}

func TestSessionStoreConcurrentSetCookies(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)

	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j.SetCookies(base, []*http.Cookie{cookieFor("sid", string(rune('a'+i%26)))})
		}()
	}
	wg.Wait()

	path := filepath.Join(dir, sessionFileName(base))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf sessionFile
	require.NoError(t, json.Unmarshal(data, &sf))
	assert.Len(t, sf.Cookies, 1)
	live := j.Cookies(base)
	reopened := openPersistentJar(base, dir).Cookies(base)
	assert.Equal(t, live, reopened, "the final persisted cookie matches the live jar")
}

func TestSessionStoreUnchangedCookieSetOneWrite(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)

	c := cookieFor("sid", "abc123")
	c.Expires = time.Now().Add(time.Hour).UTC()

	j.SetCookies(base, []*http.Cookie{c})
	path := filepath.Join(dir, sessionFileName(base))
	oldTime := time.Unix(1, 0)
	require.NoError(t, os.Chtimes(path, oldTime, oldTime))
	info1, err := os.Stat(path)
	require.NoError(t, err)

	j.SetCookies(base, []*http.Cookie{c})
	info2, err := os.Stat(path)
	require.NoError(t, err)

	assert.Equal(t, info1.ModTime(), info2.ModTime(),
		"the same cookie twice must produce one write")
}

// An unchanged cookie must not rewrite the file, expiry or not. The expiry
// is a *time.Time, so comparing the records structurally compares pointers
// and would call this cookie changed on every re-set.
func TestSessionStoreUnchangedCookieWithExpirySetOneWrite(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	path := filepath.Join(dir, sessionFileName(base))

	expires := time.Now().Add(time.Hour).UTC()
	sf := sessionFile{
		HubURL:  base.String(),
		Cookies: []persistedCookie{{Name: "sid", Value: "abc123", Path: "/", Expires: &expires}},
	}
	// The compact form differs from the writer's own indented form, so a
	// rewrite changes these bytes even though the cookie values are the same.
	data, err := json.Marshal(sf)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, atomicfile.WriteFile(path, data, 0o600))

	j := openPersistentJar(base, dir)
	j.SetCookies(base, []*http.Cookie{{Name: "sid", Value: "abc123", Path: "/", Expires: expires}})

	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, data, got, "an unchanged cookie must not rewrite the session file")
}

// Equal-path cookies use creation order as their final tie breaker. The file
// preserves that order, so a snapshot or restart chooses the same value.
func TestSessionStoreCookieFilePreservesCreationOrder(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)

	j.SetCookies(base, []*http.Cookie{
		{Name: "y", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/z"},
		{Name: "sid", Value: "v", Path: "/deep"},
		{Name: "m", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/"},
		{Name: "x", Value: "v", Path: "/"},
		{Name: "sid", Value: "v", Path: "/"},
		{Name: "q", Value: "v", Path: "/"},
		{Name: "w", Value: "v", Path: "/"},
		{Name: "b", Value: "v", Path: "/"},
		{Name: "v", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/deep/deeper"},
	})

	path := filepath.Join(dir, sessionFileName(base))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var sf sessionFile
	require.NoError(t, json.Unmarshal(data, &sf))
	want := []persistedCookie{
		{Name: "y", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/z"},
		{Name: "sid", Value: "v", Path: "/deep"},
		{Name: "m", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/"},
		{Name: "x", Value: "v", Path: "/"},
		{Name: "sid", Value: "v", Path: "/"},
		{Name: "q", Value: "v", Path: "/"},
		{Name: "w", Value: "v", Path: "/"},
		{Name: "b", Value: "v", Path: "/"},
		{Name: "v", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/deep/deeper"},
	}
	assert.Equal(t, want, sf.Cookies, "the session file must preserve cookie creation order")
}

func TestSessionStoreRetriesAnIdenticalCookieAfterAWriteFailure(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	jar := openPersistentJar(base, dir)
	path := filepath.Join(dir, sessionFileName(base))
	cookie := cookieFor("sid", "new-session")

	require.NoError(t, os.Mkdir(path, 0o700))
	jar.SetCookies(base, []*http.Cookie{cookie})
	require.NoError(t, os.Remove(path))

	jar.SetCookies(base, []*http.Cookie{cookie})

	restored := openPersistentJar(base, dir).Cookies(base)
	require.Len(t, restored, 1)
	assert.Equal(t, "new-session", restored[0].Value)
}

func TestSessionStoreRetriesAnIdenticalClearAfterARemovalFailure(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	jar := openPersistentJar(base, dir)
	path := filepath.Join(dir, sessionFileName(base))
	jar.SetCookies(base, []*http.Cookie{cookieFor("sid", "old-session")})
	require.NoError(t, os.Remove(path))
	require.NoError(t, os.Mkdir(path, 0o700))
	blocker := filepath.Join(path, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("keep this directory nonempty"), 0o600))

	deletion := &http.Cookie{Name: "sid", Path: "/", MaxAge: -1}
	jar.SetCookies(base, []*http.Cookie{deletion})
	require.NoError(t, os.Remove(blocker))

	jar.SetCookies(base, []*http.Cookie{deletion})

	_, err := os.Stat(path)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestSessionStorePreservesPrecedenceForSameNameAndPath(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example.test")
	jar := openPersistentJar(base, dir)
	jar.SetCookies(base, []*http.Cookie{
		{Name: "scope", Value: "parent-domain", Path: "/", Domain: ".example.test"},
		cookieFor("scope", "host-only"),
	})

	winner := func(view http.CookieJar) string {
		t.Helper()
		cookies := view.Cookies(base)
		require.Len(t, cookies, 2)
		return cookies[0].Value
	}
	require.Equal(t, "parent-domain", winner(jar))
	assert.Equal(t, winner(jar), winner(jar.snapshot()))
	assert.Equal(t, winner(jar), winner(openPersistentJar(base, dir)))
}

func TestSessionStoreCanPersistForALongDNSName(t *testing.T) {
	dir := privateDir(t)
	host := strings.Join([]string{
		strings.Repeat("a", 63), strings.Repeat("b", 63),
		strings.Repeat("c", 63), strings.Repeat("d", 61),
	}, ".")
	base := mustParse(t, "https://"+host)
	jar := openPersistentJar(base, dir)
	jar.SetCookies(base, []*http.Cookie{cookieFor("sid", "new-session")})

	restored := openPersistentJar(base, dir).Cookies(base)
	require.Len(t, restored, 1)
	assert.Equal(t, "new-session", restored[0].Value)
}
