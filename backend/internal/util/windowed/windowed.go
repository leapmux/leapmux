// Package windowed is the fixed-window counter core two limiters share:
// the hub's rate limiter (per operation and user) and the per-recipient
// mail budget. Both spell one policy over the same mechanics -- an entry
// anchors at first use, self-expires one window later, and a sweep drops
// what expired -- and the mechanics had drifted once already (the sweep's
// earliest-expiry gate had to be fixed on each side separately), so they
// live here once.
//
// Windows is not thread-safe by itself: every caller owns a mutex and
// documents it, because the two limiters lock at different scopes (one
// manager-wide lock; one per sender).
package windowed

import "time"

// Entry is one key's fixed window: the counter the caller's policy spends
// and the instant the window closes. Count names no policy -- failures,
// admitted requests, delivered mails -- because each caller counts a
// different quantity over the same mechanics.
type Entry struct {
	Count   int64
	ResetAt time.Time
}

// Windows holds one Entry per key with self-expiring fixed windows.
type Windows[K comparable] struct {
	entries map[K]*Entry
	// minResetAt is the earliest ResetAt among the live entries; the zero
	// value means the map holds none. It keeps the sweep off the hot path:
	// a full traversal runs only when a window actually expired.
	minResetAt time.Time
}

// Get returns key's live entry, or nil when none exists or its window
// expired. It anchors nothing: a caller that peeks before deciding to
// spend (the rate limiter's admission check) must not open a window by
// looking. An expired entry stays in the map until the next Sweep drops
// it, which Sweep's own gate guarantees runs at the first call after its
// window closed.
func (w *Windows[K]) Get(key K, now time.Time) *Entry {
	e := w.entries[key]
	if e == nil || now.After(e.ResetAt) {
		return nil
	}
	return e
}

// Anchor returns key's entry, opening a fresh window that closes one
// window-length from now when none is live. This is the only way an entry
// enters the map, so an entry's ResetAt is never rewritten while live.
func (w *Windows[K]) Anchor(key K, now time.Time, window time.Duration) *Entry {
	if e := w.entries[key]; e != nil && !now.After(e.ResetAt) {
		return e
	}
	if w.entries == nil {
		w.entries = make(map[K]*Entry)
	}
	e := &Entry{ResetAt: now.Add(window)}
	w.entries[key] = e
	w.trackResetAt(e.ResetAt)
	return e
}

// Delete drops key's entry. The caller that proves a budget should clear
// (a verified credential) uses this; nothing else needs it, because
// expiry is Sweep's job.
func (w *Windows[K]) Delete(key K) {
	delete(w.entries, key)
}

// Sweep drops every expired entry. It runs its full traversal only when
// the earliest live window expired (minResetAt says a deletion is due), so
// a call on a map of live windows costs one lookup, never a traversal --
// while every expired entry still leaves at the first call after its
// window closed, because minResetAt is the minimum over all entries.
func (w *Windows[K]) Sweep(now time.Time) {
	if w.minResetAt.IsZero() || now.Before(w.minResetAt) {
		return
	}
	next := time.Time{}
	for k, e := range w.entries {
		if now.After(e.ResetAt) {
			delete(w.entries, k)
			continue
		}
		if next.IsZero() || e.ResetAt.Before(next) {
			next = e.ResetAt
		}
	}
	w.minResetAt = next
}

// Len reports how many entries the map holds, live or expired-but-unswept.
func (w *Windows[K]) Len() int {
	return len(w.entries)
}

// trackResetAt records an anchored window's close as the next earliest
// when it is one.
func (w *Windows[K]) trackResetAt(resetAt time.Time) {
	if w.minResetAt.IsZero() || resetAt.Before(w.minResetAt) {
		w.minResetAt = resetAt
	}
}
