import { createStore } from 'solid-js/store'
import { clearStaleKeys } from './clearStaleKeys'

// ---------------------------------------------------------------------------
// Per-message UI annotation slices
//
// Two id-keyed string maps shown beneath a message bubble:
//  - errors: a send's delivery-error text (a FAILED send), set by the windowing
//    core (addMessage / setMessages) and the send path, cleared on removal.
//  - pendingLabels: a non-error label for an optimistic bubble held in the
//    startup queue until the agent transitions to ACTIVE.
// Independent of the windowing invariants, so they own their own reactive slices.
// Both are the same shape -- an id-keyed string map with set / single-clear /
// batched-guarded-clear -- so they share the createIdStringMap factory rather than
// re-spelling the createStore + set/clear/clearMany trio twice.
// ---------------------------------------------------------------------------

interface IdStringMap {
  /** The reactive id -> string map (read whole or by id for reactivity). */
  map: Record<string, string>
  /** Set a row's value. */
  set: (id: string, value: string) => void
  /** Clear a single row's value. */
  clear: (id: string) => void
  /**
   * Batched, guarded clear of many rows (a trim / full-window replace / page merge
   * dropping a batch). Guarded to the ids that actually carry a value -- the common
   * drop of value-free rows is a no-op that doesn't churn the store -- via the shared
   * clearStaleKeys spine.
   */
  clearMany: (ids: Iterable<string>) => void
}

function createIdStringMap(): IdStringMap {
  const [map, setMap] = createStore<Record<string, string>>({})
  return {
    map,
    set: (id, value) => setMap(id, value),
    clear: id => setMap(id, undefined!),
    clearMany: ids => clearStaleKeys(map, setMap, ids),
  }
}

export function createMessageAnnotationStore() {
  const errors = createIdStringMap()
  const pendingLabels = createIdStringMap()
  return {
    /** The reactive id -> error map (read whole or by id). */
    errors: errors.map,
    /** The reactive id -> pending-label map (read whole or by id). */
    pendingLabels: pendingLabels.map,
    setError: errors.set,
    clearError: errors.clear,
    clearErrors: errors.clearMany,
    setPendingLabel: pendingLabels.set,
    clearPendingLabel: pendingLabels.clear,
    clearPendingLabels: pendingLabels.clearMany,
  }
}
