package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/ctxutil"
	"github.com/leapmux/leapmux/util/useronly"
)

// persistedCookie is the on-disk form of one cookie.
type persistedCookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Path     string     `json:"path"`
	Domain   string     `json:"domain,omitempty"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure,omitempty"`
	HTTPOnly bool       `json:"http_only,omitempty"`
}

// equal reports whether the two records hold the same cookie values. The
// expiry compares by instant: the field is a pointer, so struct equality
// would report a cookie with an expiry as changed whenever it is re-set.
func (pc persistedCookie) equal(other persistedCookie) bool {
	if pc.Name != other.Name || pc.Value != other.Value || pc.Path != other.Path || pc.Domain != other.Domain ||
		pc.Secure != other.Secure || pc.HTTPOnly != other.HTTPOnly {
		return false
	}
	if pc.Expires == nil || other.Expires == nil {
		return pc.Expires == nil && other.Expires == nil
	}
	return pc.Expires.Equal(*other.Expires)
}

// sessionFile is the on-disk form of a session record.
type sessionFile struct {
	HubURL  string            `json:"hub_url"`
	Cookies []persistedCookie `json:"cookies"`
}

// persistentJar wraps cookiejar.Jar and persists the Hub's cookies to a JSON
// file. Cookies for the Hub's origin are recorded and written on change; the
// file survives a restart so the session survives with it.
type persistentJar struct {
	inner http.CookieJar

	mu        sync.Mutex
	base      *url.URL
	path      string // empty means memory only
	record    map[sessionCookieKey]persistedCookie
	order     map[sessionCookieKey]uint64
	nextOrder uint64
	lastJSON  string // last marshalled record; skip write when unchanged
	diskDirty bool
}

// newHubCookieJar creates an in-memory jar and its record for exchange snapshots.
func newHubCookieJar(base *url.URL) *persistentJar {
	return &persistentJar{
		inner:  mustNewMemoryJar(),
		base:   base,
		record: make(map[sessionCookieKey]persistedCookie),
		order:  make(map[sessionCookieKey]uint64),
	}
}

// openPersistentJar returns a jar that persists Hub-origin cookies to a file
// under dir. It never fails: any problem leaves the jar empty in memory and
// logs a warning. dir may be empty, in which case the jar is memory only.
func openPersistentJar(base *url.URL, dir string) *persistentJar {
	j := newHubCookieJar(base)
	if dir == "" {
		slog.Warn("session jar: no sessions directory; keeping cookies in memory only")
		return j
	}
	j.path = filepath.Join(dir, sessionFileName(base))
	if err := atomicfile.RemoveTempFiles(j.path); err != nil {
		slog.Warn("session jar: remove temp files", "path", j.path, "err", err)
	}
	data, err := useronly.ReadFile(j.path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("session jar: cannot read session file; starting empty", "path", j.path, "error", err)
		}
		return j
	}
	var sf sessionFile
	if err := json.Unmarshal(data, &sf); err != nil {
		slog.Warn("session jar: session file does not parse; starting empty", "path", j.path, "error", err)
		return j
	}
	hubURL, err := url.Parse(sf.HubURL)
	if err != nil || !sameHubOrigin(hubURL, base) {
		slog.Warn("session jar: session file is for another hub; starting empty", "path", j.path, "hub_url", sf.HubURL)
		return j
	}
	now := time.Now()
	for _, pc := range sf.Cookies {
		if pc.Expires != nil && !pc.Expires.After(now) {
			continue
		}
		c := pc.toHTTP()
		key := cookieKey(base, &c)
		j.record[key] = pc
		j.order[key] = j.nextOrder
		j.nextOrder++
		j.inner.SetCookies(base, []*http.Cookie{&c})
	}
	j.lastJSON = string(data)
	return j
}

// sessionsDir returns the sessions directory beside the config file, or the
// empty string when the config path is unknown.
func sessionsDir() string {
	p, err := configPath()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "sessions")
}

// sessionFileName returns a fixed-size file name derived from base's origin.
// The file stores the complete Hub URL and rejects a digest collision on read.
func sessionFileName(base *url.URL) string {
	origin, err := canonicalHubOrigin(base)
	if err != nil {
		origin = strings.ToLower(base.String())
	}
	digest := sha256.Sum256([]byte(origin))
	return fmt.Sprintf("session_%x.json", digest)
}

// SetCookies forwards to the inner jar and, for the Hub's origin, records each
// cookie and persists the record when it changed.
func (j *persistentJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	if !sameHubOrigin(u, j.base) {
		j.inner.SetCookies(u, cookies)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	j.inner.SetCookies(u, cookies)
	changed := false
	for _, c := range cookies {
		if c.Domain != "" && !acceptsCookieDomain(u, c.Domain) {
			continue
		}
		k := cookieKey(u, c)
		pc, expired := fromHTTP(c, k.path, now)
		if expired {
			if _, ok := j.record[k]; ok {
				delete(j.record, k)
				delete(j.order, k)
				changed = true
			}
			continue
		}
		if old, ok := j.record[k]; ok && old.equal(pc) {
			continue
		}
		if _, ok := j.record[k]; !ok {
			j.order[k] = j.nextOrder
			j.nextOrder++
		}
		j.record[k] = pc
		changed = true
	}
	if changed {
		j.diskDirty = true
	}
	if !j.diskDirty {
		return
	}
	j.persistLocked()
}

// Cookies returns the cookies the inner jar holds for u.
func (j *persistentJar) Cookies(u *url.URL) []*http.Cookie {
	return j.inner.Cookies(u)
}

type sessionCookieKey struct {
	name   string
	path   string
	domain string
}

// cookieKey uses the effective path and domain because updates can omit the
// Path attribute or replace a host-only cookie with a cookie for that host.
func cookieKey(u *url.URL, c *http.Cookie) sessionCookieKey {
	domain := c.Domain
	if domain == "" {
		domain = u.Hostname()
	}
	return sessionCookieKey{
		name:   c.Name,
		path:   sessionCookiePath(u, c.Path),
		domain: strings.ToLower(strings.TrimPrefix(domain, ".")),
	}
}

func sessionCookiePath(u *url.URL, path string) string {
	if strings.HasPrefix(path, "/") {
		return path
	}
	// Use the same default path as net/http/cookiejar.
	if lastSlash := strings.LastIndexByte(u.Path, '/'); lastSlash > 0 {
		return u.Path[:lastSlash]
	}
	return "/"
}

// acceptsCookieDomain uses the standard jar's domain rules even for a
// deletion. An invalid domain cannot displace a cookie in the record.
func acceptsCookieDomain(u *url.URL, domain string) bool {
	probe := mustNewMemoryJar()
	probe.SetCookies(u, []*http.Cookie{{Name: "domain", Value: "test", Domain: domain, Path: "/"}})
	return len(probe.Cookies(u)) != 0
}

// snapshot gives a token exchange its own standard jar, seeded from the
// record before it sends a request. A redirect can then read staged changes
// without exposing them to the live proxy.
func (j *persistentJar) snapshot() http.CookieJar {
	j.mu.Lock()
	defer j.mu.Unlock()

	snapshot := mustNewMemoryJar()
	cookies := make([]*http.Cookie, 0, len(j.record))
	for _, record := range j.orderedCookies() {
		cookie := record.toHTTP()
		cookies = append(cookies, &cookie)
	}
	snapshot.SetCookies(j.base, cookies)
	return snapshot
}

// persistLocked writes or removes the session file. The caller holds j.mu.
func (j *persistentJar) persistLocked() {
	if j.path == "" {
		j.diskDirty = false
		return
	}
	// The inner jar drops a clock-expired cookie at read time, but the
	// record only removes one when a Set-Cookie deletes it. Prune the rows
	// whose expiry has passed, so the file never holds a credential the jar
	// would not return.
	now := time.Now()
	for k, pc := range j.record {
		if pc.Expires != nil && !pc.Expires.After(now) {
			delete(j.record, k)
			delete(j.order, k)
			j.diskDirty = true
		}
	}
	if len(j.record) == 0 {
		if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
			slog.Warn("session store: remove session file", "path", j.path, "err", err)
			return
		}
		if err := atomicfile.RemoveTempFiles(j.path); err != nil {
			slog.Warn("session store: remove temp files", "path", j.path, "err", err)
			return
		}
		// The removal leaves no file behind, so drop the memo of the
		// last write: a later change that marshals to those same bytes
		// must still write.
		j.lastJSON = ""
		j.diskDirty = false
		return
	}
	data, err := json.MarshalIndent(sessionFile{HubURL: j.base.String(), Cookies: j.orderedCookies()}, "", "  ")
	if err != nil {
		slog.Warn("session jar: cannot encode session record; keeping cookies in memory only", "error", err)
		return
	}
	if !j.diskDirty && string(data) == j.lastJSON {
		return
	}
	dir := filepath.Dir(j.path)
	if err := atomicfile.MkdirAllPrivate(dir); err != nil {
		slog.Warn("session jar: cannot create sessions directory; keeping cookies in memory only", "dir", dir, "error", err)
		return
	}
	if err := atomicfile.WriteFile(j.path, data, 0o600); err != nil {
		slog.Warn("session jar: cannot write session file; keeping cookies in memory only", "path", j.path, "error", err)
		return
	}
	j.lastJSON = string(data)
	j.diskDirty = false
}

// orderedCookies preserves the standard jar's creation-order tie breaker.
// The caller holds j.mu.
func (j *persistentJar) orderedCookies() []persistedCookie {
	type orderedCookie struct {
		cookie persistedCookie
		order  uint64
	}
	ordered := make([]orderedCookie, 0, len(j.record))
	for key, cookie := range j.record {
		ordered = append(ordered, orderedCookie{cookie: cookie, order: j.order[key]})
	}
	slices.SortFunc(ordered, func(a, b orderedCookie) int {
		return cmp.Compare(a.order, b.order)
	})
	cookies := make([]persistedCookie, len(ordered))
	for index, item := range ordered {
		cookies[index] = item.cookie
	}
	return cookies
}

// fromHTTP converts a cookie to its persisted form and reports whether it
// removes the cookie. Max-Age overrides Expires, as in net/http/cookiejar.
func fromHTTP(c *http.Cookie, path string, now time.Time) (pc persistedCookie, expired bool) {
	if c.MaxAge < 0 {
		return persistedCookie{}, true
	}
	expires := c.Expires
	if c.MaxAge > 0 {
		expires = now.Add(time.Duration(c.MaxAge) * time.Second)
	}
	if !expires.IsZero() && !expires.After(now) {
		return persistedCookie{}, true
	}

	pc = persistedCookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     path,
		Domain:   c.Domain,
		Secure:   c.Secure,
		HTTPOnly: c.HttpOnly,
	}
	if !expires.IsZero() {
		t := expires.UTC()
		pc.Expires = &t
	}
	return pc, false
}

// toHTTP converts a persisted cookie back to http.Cookie.
func (pc *persistedCookie) toHTTP() http.Cookie {
	c := http.Cookie{
		Name:     pc.Name,
		Value:    pc.Value,
		Path:     pc.Path,
		Domain:   pc.Domain,
		Secure:   pc.Secure,
		HttpOnly: pc.HTTPOnly,
	}
	if pc.Expires != nil {
		c.Expires = *pc.Expires
	}
	return c
}

// sessionFileAccess orders an admitted browser sign-in with reopening its
// session file. Its mutex only protects the active locks, so slow disk work
// for one Hub never holds the App's lifecycle mutex or delays another Hub.
type sessionFileAccess struct {
	mu    sync.Mutex
	paths map[string]*sessionFileLock
}

type sessionFileLock struct {
	mutex ctxutil.Mutex
	users int
}

func (s *sessionFileAccess) lock(ctx context.Context, key string) (func(), error) {
	s.mu.Lock()
	if s.paths == nil {
		s.paths = make(map[string]*sessionFileLock)
	}
	entry := s.paths[key]
	if entry == nil {
		entry = &sessionFileLock{}
		s.paths[key] = entry
	}
	entry.users++
	s.mu.Unlock()

	release := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(s.paths, key)
		}
	}
	if err := entry.mutex.Lock(ctx); err != nil {
		release()
		return nil, err
	}
	return func() {
		entry.mutex.Unlock()
		release()
	}, nil
}
