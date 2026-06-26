import type { SetStoreFunction } from 'solid-js/store'
import { produce } from 'solid-js/store'

/**
 * Delete many ids from an id-keyed reactive `Record` in a single batched
 * update, skipping ids that aren't present so the common drop of rows that
 * never held the value is a no-op that doesn't churn the store. The shared
 * spine of the per-id UI side-state maps a structural window drop must reclaim
 * -- error annotations, pending-label annotations, and content-version
 * counters -- so the guarded-batched-delete semantics live in one place instead
 * of being copied per map.
 */
export function clearStaleKeys<T>(
  record: Record<string, T>,
  setRecord: SetStoreFunction<Record<string, T>>,
  ids: Iterable<string>,
): void {
  const stale = [...ids].filter(id => record[id] !== undefined)
  if (stale.length === 0)
    return
  setRecord(produce((map) => {
    for (const id of stale)
      delete map[id]
  }))
}
