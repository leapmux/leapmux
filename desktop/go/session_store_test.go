package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
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

	require.NoError(t, os.WriteFile(tmpPath, []byte("leftover"), 0o600))
	j := openPersistentJar(base, dir)
	j.SetCookies(base, []*http.Cookie{cookieFor("sid", "abc")})
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

func TestSessionStoreUnsafeBytesEncoded(t *testing.T) {
	a := mustParse(t, "https://a_b.example")
	b := mustParse(t, "https://a~b.example")
	ipv6 := mustParse(t, "http://[::1]")

	assert.NotEqual(t, sessionFileName(a), sessionFileName(b),
		"hosts that differ in an unsafe byte must get different files")
	assert.Equal(t, "http__3a_3a1_80.json", sessionFileName(ipv6),
		"[::1] encodes to _3a_3a1 (url.Hostname strips brackets)")
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
}

func TestSessionStoreUnchangedCookieSetOneWrite(t *testing.T) {
	dir := privateDir(t)
	base := mustParse(t, "https://hub.example")
	j := openPersistentJar(base, dir)

	c := cookieFor("sid", "abc123")
	c.Expires = time.Now().Add(time.Hour).UTC()

	j.SetCookies(base, []*http.Cookie{c})
	path := filepath.Join(dir, sessionFileName(base))
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

// The record is a map, so marshalling it in iteration order would give a
// different byte sequence on every write. The cookies are sorted by name and
// path first, so the same record always writes the same bytes.
func TestSessionStoreCookieFileOrderIsStable(t *testing.T) {
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
		{Name: "a", Value: "v", Path: "/"},
		{Name: "a", Value: "v", Path: "/deep/deeper"},
		{Name: "a", Value: "v", Path: "/z"},
		{Name: "b", Value: "v", Path: "/"},
		{Name: "m", Value: "v", Path: "/"},
		{Name: "q", Value: "v", Path: "/"},
		{Name: "sid", Value: "v", Path: "/"},
		{Name: "sid", Value: "v", Path: "/deep"},
		{Name: "v", Value: "v", Path: "/"},
		{Name: "w", Value: "v", Path: "/"},
		{Name: "x", Value: "v", Path: "/"},
		{Name: "y", Value: "v", Path: "/"},
	}
	assert.Equal(t, want, sf.Cookies, "the session file must hold the cookies sorted by name and path")
}
