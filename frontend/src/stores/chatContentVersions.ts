import { createStore } from 'solid-js/store'
import { clearStaleKeys } from './clearStaleKeys'

// ---------------------------------------------------------------------------
// Per-message content-version counters
//
// A row's version is bumped on the rare in-place same-seq merge, which replaces a
// message's content but leaves the fields the classified-entry cache's memo subscribes
// to (seq / id / spanId) untouched -- so the memo would NOT wake on its own. Reading
// the version reactively per row is what makes the bump wake the memo to re-check
// freshness (a tool_result also folds in its OPENER's version, since an opener body
// replacement must bust the result's stale classification / estimate).
//
// Keyed by message id and written ONLY by that rare merge, so it stays tiny; an id-keyed
// reactive map with bump + batched-guarded forget, the same per-id-side-state-reclaimed
// shape as the annotation slices. Owns its own reactive slice, independent of the
// windowing invariants.
// ---------------------------------------------------------------------------

export function createContentVersionStore() {
  const [versions, setVersions] = createStore<Record<string, number>>({})
  return {
    /** The current content version for a row (0 when it was never in-place-merged). */
    get: (id: string): number => versions[id] ?? 0,
    /** Bump a row's version: an in-place same-seq merge replaced its content. */
    bump: (id: string) => setVersions(id, (versions[id] ?? 0) + 1),
    /**
     * Drop the counters of rows leaving the window for good (a delete or trim).
     * Guarded so the common removal of rows that never got an in-place update is a
     * no-op that doesn't churn the store, via the shared clearStaleKeys spine.
     */
    forget: (ids: Iterable<string>) => clearStaleKeys(versions, setVersions, ids),
  }
}
