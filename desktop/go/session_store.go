package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/leapmux/leapmux/util/atomicfile"
	"github.com/leapmux/leapmux/util/useronly"
)

// persistedCookie is the on-disk form of one cookie.
type persistedCookie struct {
	Name     string     `json:"name"`
	Value    string     `json:"value"`
	Path     string     `json:"path"`
	Expires  *time.Time `json:"expires,omitempty"`
	Secure   bool       `json:"secure,omitempty"`
	HTTPOnly bool       `json:"http_only,omitempty"`
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
	inner *cookiejar.Jar

	mu       sync.Mutex
	base     *url.URL
	path     string // empty means memory only
	record   map[string]persistedCookie
	lastJSON string // last marshalled record; skip write when unchanged
}

// openPersistentJar returns a jar that persists Hub-origin cookies to a file
// under dir. It never fails: any problem leaves the jar empty in memory and
// logs a warning. dir may be empty, in which case the jar is memory only.
func openPersistentJar(base *url.URL, dir string) *persistentJar {
	inner, _ := cookiejar.New(nil)
	j := &persistentJar{
		inner:  inner,
		base:   base,
		record: map[string]persistedCookie{},
	}
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
		if pc.Expires != nil && pc.Expires.Before(now) {
			continue
		}
		c := pc.toHTTP()
		j.record[cookieKey(&c)] = pc
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

// sessionFileName returns the file name for base's origin:
// <scheme>_<host>_<port>.json, with the host lower-cased and each unsafe byte
// written as _ plus two hex digits.
func sessionFileName(base *url.URL) string {
	return base.Scheme + "_" + encodeHost(base.Hostname()) + "_" + effectivePort(base) + ".json"
}

// encodeHost lower-cases the host and encodes every byte outside [a-z0-9.-]
// as _ plus two hex digits. Two different origins never share a file name.
func encodeHost(host string) string {
	host = strings.ToLower(host)
	var sb strings.Builder
	for i := 0; i < len(host); i++ {
		b := host[i]
		switch {
		case b >= 'a' && b <= 'z', b >= '0' && b <= '9', b == '.', b == '-':
			sb.WriteByte(b)
		default:
			sb.WriteByte('_')
			const hexDigits = "0123456789abcdef"
			sb.WriteByte(hexDigits[b>>4])
			sb.WriteByte(hexDigits[b&0x0f])
		}
	}
	return sb.String()
}

// SetCookies forwards to the inner jar and, for the Hub's origin, records each
// cookie and persists the record when it changed.
func (j *persistentJar) SetCookies(u *url.URL, cookies []*http.Cookie) {
	j.inner.SetCookies(u, cookies)
	if !sameHubOrigin(u, j.base) {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	changed := false
	for _, c := range cookies {
		if cookieExpired(c) {
			k := cookieKey(c)
			if _, ok := j.record[k]; ok {
				delete(j.record, k)
				changed = true
			}
			continue
		}
		k := cookieKey(c)
		pc := fromHTTP(c)
		if old, ok := j.record[k]; ok && old == pc {
			continue
		}
		j.record[k] = pc
		changed = true
	}
	if !changed {
		return
	}
	j.persistLocked()
}

// Cookies returns the cookies the inner jar holds for u.
func (j *persistentJar) Cookies(u *url.URL) []*http.Cookie {
	return j.inner.Cookies(u)
}

// cookieKey identifies a cookie by name and path, matching cookiejar's own
// storage key for a given host.
func cookieKey(c *http.Cookie) string {
	return c.Name + "|" + c.Path
}

// cookieExpired reports whether a Set-Cookie removes the cookie: MaxAge < 0
// means delete now (net/http maps Max-Age=0 to -1), and an Expires in the past
// means the cookie is already dead.
func cookieExpired(c *http.Cookie) bool {
	if c.MaxAge < 0 {
		return true
	}
	return !c.Expires.IsZero() && c.Expires.Before(time.Now())
}

// persistLocked writes or removes the session file. The caller holds j.mu.
func (j *persistentJar) persistLocked() {
	if j.path == "" {
		return
	}
	if len(j.record) == 0 {
		if err := os.Remove(j.path); err != nil && !os.IsNotExist(err) {
			slog.Warn("session store: remove session file", "path", j.path, "err", err)
		}
		if err := atomicfile.RemoveTempFiles(j.path); err != nil {
			slog.Warn("session store: remove temp files", "path", j.path, "err", err)
		}
		return
	}
	cookies := make([]persistedCookie, 0, len(j.record))
	for _, pc := range j.record {
		cookies = append(cookies, pc)
	}
	data, err := json.MarshalIndent(sessionFile{HubURL: j.base.String(), Cookies: cookies}, "", "  ")
	if err != nil {
		slog.Warn("session jar: cannot encode session record; keeping cookies in memory only", "error", err)
		return
	}
	if string(data) == j.lastJSON {
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
}

// fromHTTP converts an http.Cookie to its persisted form.
func fromHTTP(c *http.Cookie) persistedCookie {
	pc := persistedCookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     c.Path,
		Secure:   c.Secure,
		HTTPOnly: c.HttpOnly,
	}
	if !c.Expires.IsZero() {
		t := c.Expires.UTC()
		pc.Expires = &t
	}
	return pc
}

// toHTTP converts a persisted cookie back to http.Cookie.
func (pc *persistedCookie) toHTTP() http.Cookie {
	c := http.Cookie{
		Name:     pc.Name,
		Value:    pc.Value,
		Path:     pc.Path,
		Secure:   pc.Secure,
		HttpOnly: pc.HTTPOnly,
	}
	if pc.Expires != nil {
		c.Expires = *pc.Expires
	}
	return c
}
