import type { Accessor } from 'solid-js'
import type { PersistableRowHeight, UseChatVirtualizerResult, VirtualItem } from './useChatVirtualizer'
import { createEffect, onCleanup, untrack } from 'solid-js'
import { localStorageGet, localStorageSet, PREFIX_CHAT_ROW_HEIGHTS } from '~/lib/browserStorage'
import { fnv1a32Hex } from '~/lib/stringDigest'

/**
 * Reload warm-start for the virtualizer's measured-height cache.
 *
 * Without persistence every reload reopens the chat with nothing but
 * per-kind estimates: the whole premeasure pipeline re-renders and
 * re-measures the window, and every estimate->real correction shifts the
 * offset map under the scroll anchor. Persisting (id, heightKey digest,
 * height) triples lets a reload adopt real geometry immediately, so the
 * premeasure churn only happens for rows whose content or layout actually
 * changed.
 *
 * Correctness model: heights are keyed by the row's full heightKey, which
 * already folds in the content width, layout-affecting preferences, and
 * content/UI versions (see ChatView's virtualItems). An entry hydrates only
 * when the digest of the row's LIVE key matches, so a stored height can
 * never be adopted for different content or a different pane width — a
 * mismatch just falls back to today's estimate behavior. Keys are long, so
 * only their fnv1a digest is stored; a (astronomically unlikely) collision
 * adopts a wrong warm-start height, which the row's real measurement then
 * corrects on mount — the same self-healing path premeasure already relies
 * on. Entries that don't match YET (e.g. the pane width hasn't settled, or
 * older history hasn't paginated in) stay pending and are retried whenever
 * the item list changes.
 */

/** One stored row: [message id, heightKey digest, height (px, 2dp)]. */
type StoredRow = [id: string, digest: string, height: number]

interface StoredRowHeights {
  v: 1
  rows: StoredRow[]
}

/**
 * Storage cap, matching the in-memory window ceiling
 * (MAX_LOADED_CHAT_MESSAGES_CEILING): persisting more than the window can
 * ever hold buys nothing.
 */
export const PERSISTED_ROW_HEIGHTS_MAX = 1200

/**
 * Save debounce. Measurements arrive in bursts (a premeasure band, a resize
 * re-measure sweep); one write per quiet second keeps localStorage traffic
 * negligible while staying well inside "toggle the sidebar then reload".
 */
export const ROW_HEIGHT_SAVE_DEBOUNCE_MS = 1000

export interface RowHeightPersistenceDeps {
  /**
   * Stable per-chat identity for the storage key (the agent id). Undefined
   * disables persistence entirely (nothing loads, nothing saves).
   */
  storageId: () => string | undefined
  virtualItems: Accessor<VirtualItem[]>
  virt: Pick<UseChatVirtualizerResult, 'primeHeights' | 'snapshotHeights' | 'geometryVersion' | 'hasMeasuredHeight'>
}

function parseStoredRows(raw: unknown): Map<string, { digest: string, height: number }> {
  const pending = new Map<string, { digest: string, height: number }>()
  if (raw === null || typeof raw !== 'object' || (raw as StoredRowHeights).v !== 1)
    return pending
  const rows = (raw as StoredRowHeights).rows
  if (!Array.isArray(rows))
    return pending
  for (const row of rows) {
    if (!Array.isArray(row) || row.length !== 3)
      continue
    const [id, digest, height] = row as unknown[]
    if (typeof id !== 'string' || id.length === 0 || typeof digest !== 'string')
      continue
    if (typeof height !== 'number' || !(height > 0) || !Number.isFinite(height))
      continue
    pending.set(id, { digest, height })
  }
  return pending
}

/**
 * Wire height persistence to a virtualizer. Must be called inside a
 * reactive root (it creates effects and an onCleanup). Loading, hydration
 * retries, and debounced saving all happen internally.
 */
export function createRowHeightPersistence(deps: RowHeightPersistenceDeps): void {
  // Entries loaded from storage that haven't hydrated yet. An entry leaves
  // when it is adopted or superseded by a real measurement; a digest
  // mismatch keeps it pending, because the layout epoch it was measured
  // under may still arrive (width settling, pagination).
  const pending = new Map<string, { digest: string, height: number }>()
  let loaded = false
  let saveTimer: ReturnType<typeof setTimeout> | undefined

  const storageKey = (id: string) => `${PREFIX_CHAT_ROW_HEIGHTS}${id}`

  const tryAdopt = (items: readonly VirtualItem[]): void => {
    if (pending.size === 0)
      return
    const adoptable: PersistableRowHeight[] = []
    for (const item of items) {
      const entry = pending.get(item.id)
      if (entry === undefined)
        continue
      // A real measurement (visible or premeasure) supersedes the stored
      // warm-start value — retire the entry instead of re-priming.
      if (deps.virt.hasMeasuredHeight(item.id)) {
        pending.delete(item.id)
        continue
      }
      if (fnv1a32Hex(item.heightKey ?? '') !== entry.digest)
        continue
      adoptable.push({ id: item.id, heightKey: item.heightKey, height: entry.height })
      pending.delete(item.id)
    }
    if (adoptable.length > 0)
      deps.virt.primeHeights(adoptable)
  }

  const saveNow = (id: string): void => {
    // Merge the fresh snapshot OVER the still-pending entries: rows whose
    // layout epoch hasn't matched yet (unpaginated history, unsettled
    // width) must survive the save, or the first post-reload write would
    // clobber everything the reload hadn't re-measured yet.
    const merged = new Map<string, StoredRow>()
    for (const [rowId, entry] of pending)
      merged.set(rowId, [rowId, entry.digest, entry.height])
    for (const row of deps.virt.snapshotHeights())
      merged.set(row.id, [row.id, fnv1a32Hex(row.heightKey ?? ''), Math.round(row.height * 100) / 100])
    if (merged.size === 0)
      return // never replace stored data with nothing
    let rows = [...merged.values()]
    // Pending entries were inserted first, so over-cap trimming drops them
    // before it drops freshly measured rows (snapshotHeights is LRU-ordered,
    // oldest first, so the slice keeps the most recent measurements).
    if (rows.length > PERSISTED_ROW_HEIGHTS_MAX)
      rows = rows.slice(rows.length - PERSISTED_ROW_HEIGHTS_MAX)
    localStorageSet(storageKey(id), { v: 1, rows } satisfies StoredRowHeights)
  }

  // Load once, as soon as the storage identity is available.
  createEffect(() => {
    const id = deps.storageId()
    if (id === undefined || loaded)
      return
    loaded = true
    const stored = localStorageGet<StoredRowHeights>(storageKey(id))
    if (stored !== undefined) {
      for (const [rowId, entry] of parseStoredRows(stored))
        pending.set(rowId, entry)
    }
    tryAdopt(untrack(deps.virtualItems))
  })

  // Retry hydration whenever the item list changes (pagination brings older
  // rows in; the width settling rewrites every heightKey). Everything but
  // the item read is untracked: hasMeasuredHeight would otherwise subscribe
  // this effect to every geometry bump.
  createEffect(() => {
    const items = deps.virtualItems()
    untrack(() => tryAdopt(items))
  })

  // Debounced save on geometry changes (measurement commits, hydrations).
  createEffect(() => {
    deps.virt.geometryVersion()
    const id = untrack(deps.storageId)
    if (id === undefined)
      return
    if (saveTimer !== undefined)
      clearTimeout(saveTimer)
    saveTimer = setTimeout(() => {
      saveTimer = undefined
      saveNow(id)
    }, ROW_HEIGHT_SAVE_DEBOUNCE_MS)
  })

  onCleanup(() => {
    if (saveTimer !== undefined) {
      clearTimeout(saveTimer)
      saveTimer = undefined
      // A save was owed — flush it so a tab switch right after a measurement
      // burst doesn't lose the freshest heights.
      const id = untrack(deps.storageId)
      if (id !== undefined)
        saveNow(id)
    }
  })
}
