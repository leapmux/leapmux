import type { Accessor } from 'solid-js'
import type { PersistableRowHeight, UseChatVirtualizerResult, VirtualItem } from './useChatVirtualizer'
import type { AsyncLocalKey } from '~/lib/browserStorage'
import { createEffect, onCleanup, untrack } from 'solid-js'
import { localStorageDrop, localStorageLoad, localStorageStore, PREFIX_CHAT_ROW_HEIGHTS } from '~/lib/browserStorage'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { fnv1a32Hex } from '~/lib/stringDigest'
import { MAX_LOADED_CHAT_MESSAGES_CEILING } from '~/stores/chat.store'

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

/**
 * Stored-payload version. A heightKey encodes the row's CONTENT, width and UI
 * state -- never the stylesheet -- so a layout change keeps every digest valid
 * while making every stored height wrong. Bump this whenever a change to the
 * chat CSS moves row heights, and the next load discards the whole payload
 * instead of hydrating heights that jump the offset map under the scroll anchor
 * until re-measurement heals them.
 *
 * 2: assistant messages and thoughts became full-bleed bands (row borders and
 *    band padding replaced the bubble's border, padding and 85% width cap).
 * 3: the height key became the one exact row revision key (chatRevisionKey.ts),
 *    so every stored digest identifies a key format no code builds any more. The old
 *    payload is discarded on load, never retained as a pending digest a new key
 *    cannot match.
 */
export const STORED_ROW_HEIGHTS_VERSION = 3

interface StoredRowHeights {
  v: typeof STORED_ROW_HEIGHTS_VERSION
  rows: StoredRow[]
}

/**
 * Storage cap, matching the in-memory window ceiling
 * (MAX_LOADED_CHAT_MESSAGES_CEILING): persisting more than the window can
 * ever hold buys nothing.
 */
export const PERSISTED_ROW_HEIGHTS_MAX = MAX_LOADED_CHAT_MESSAGES_CEILING

/**
 * Save debounce. Measurements arrive in bursts (a premeasure band, a resize
 * re-measure sweep); one write per quiet second keeps storage traffic
 * negligible while staying well inside "toggle the sidebar then reload".
 */
export const ROW_HEIGHT_SAVE_DEBOUNCE_MS = 1000

export interface RowHeightPersistenceDeps {
  /**
   * Stable per-chat identity for the storage key (the agent id). Undefined
   * disables persistence entirely (nothing loads, nothing saves).
   *
   * STABLE MEANS STABLE FOR THE INSTANCE'S LIFE. The load runs once and its
   * result is adopted with no identity recheck, and `pending` accumulates
   * against this one chat, so an accessor that
   * moved would adopt one chat's heights into another and save them back under
   * the wrong key. `ChatView` satisfies this: `<For>` gives each agent tab its
   * own instance and disposes it rather than re-pointing it.
   */
  storageId: () => string | undefined
  virtualItems: Accessor<VirtualItem[]>
  virt: Pick<UseChatVirtualizerResult, 'primeHeights' | 'snapshotHeights' | 'geometryVersion' | 'hasMeasuredHeight'>
}

function parseStoredRows(raw: unknown): Map<string, { digest: string, height: number }> {
  const pending = new Map<string, { digest: string, height: number }>()
  if (raw === null || typeof raw !== 'object' || (raw as StoredRowHeights).v !== STORED_ROW_HEIGHTS_VERSION)
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
    if (typeof height !== 'number' || height < 0 || !Number.isFinite(height))
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

  const storageKey = (id: string): AsyncLocalKey => `${PREFIX_CHAT_ROW_HEIGHTS}${id}`

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
      // Live key drifted off the stored digest (width/UI/chrome epoch changed): the
      // entry stays pending, because its epoch may still arrive.
      if (fnv1a32Hex(item.heightKey ?? '') !== entry.digest)
        continue
      adoptable.push({
        id: item.id,
        heightKey: item.heightKey,
        height: entry.height,
      })
    }
    if (adoptable.length === 0)
      return
    const adopted = deps.virt.primeHeights(adoptable)
    for (const entry of adoptable) {
      // An entry that the virtualizer did not adopt stays pending, and the next change
      // of the item list tries it again.
      if (adopted === adoptable.length || deps.virt.hasMeasuredHeight(entry.id))
        pending.delete(entry.id)
    }
  }

  const saveNow = (id: string): void => {
    // Merge the fresh snapshot OVER the still-pending entries: rows whose
    // layout epoch hasn't matched yet (unpaginated history, unsettled
    // width) must survive the save, or the first post-reload write would
    // clobber everything the reload hadn't re-measured yet.
    const merged = new Map<string, StoredRow>()
    for (const [rowId, entry] of pending)
      merged.set(rowId, [rowId, entry.digest, entry.height])
    for (const row of deps.virt.snapshotHeights()) {
      // delete+set (not a bare set) so a row that is ALSO still pending moves to
      // the freshly-measured end: a bare set on an existing key keeps its early
      // pending insertion position, which the over-cap slice below would shed
      // BEFORE older measurements -- dropping the newest real geometry.
      merged.delete(row.id)
      merged.set(row.id, [
        row.id,
        fnv1a32Hex(row.heightKey ?? ''),
        Math.round(row.height * 100) / 100,
      ])
    }
    if (merged.size === 0)
      return // never replace stored data with nothing
    // Pending entries were inserted first, so the shared insertion-order cap
    // (mapLru.capMapInsertionOrder) sheds them before it sheds freshly measured
    // rows (snapshotHeights is LRU-ordered, oldest first), keeping the most recent
    // measurements -- and the eviction limit matches the render and token caches.
    capMapInsertionOrder(merged, PERSISTED_ROW_HEIGHTS_MAX)
    localStorageStore(storageKey(id), {
      v: STORED_ROW_HEIGHTS_VERSION,
      rows: [
        ...merged.values(),
      ],
    } satisfies StoredRowHeights)
  }

  // Load once, as soon as the storage identity is available.
  //
  // The read is ASYNCHRONOUS -- a stored payload is tens of KB, so this family
  // is on the unmirrored tier -- so the effect returns before the rows arrive.
  //
  // ONE LOAD PER INSTANCE IS THE WHOLE CONTRACT, and it rests on `storageId`
  // being stable for the instance's life: `ChatView` passes `() => props.agentId`
  // and is rendered one instance per agent tab by a `<For>`, which binds the id
  // as a constant per row and disposes the pane rather than re-pointing it. A
  // post-await identity recheck would therefore be unreachable code, and keying
  // the latch by id would not be enough on its own to support a changing id:
  // `pending` still holds the previous chat's rows, and the debounced save would
  // write them under the new key. If `storageId` ever becomes movable, both have to
  // move with it.
  createEffect(() => {
    const id = deps.storageId()
    if (id === undefined || loaded)
      return
    loaded = true
    void localStorageLoad<StoredRowHeights>(storageKey(id)).then((stored) => {
      if (stored !== undefined) {
        const rows = parseStoredRows(stored)
        // A payload from an older version parses to nothing. Drop it now rather
        // than leaving it: the read that just returned it also refreshed its
        // expiry, so a chat that never commits a height (one the reader opens and
        // leaves) would keep tens of KB of dead rows alive indefinitely.
        if (rows.size === 0)
          localStorageDrop(storageKey(id))
        for (const [rowId, entry] of rows)
          pending.set(rowId, entry)
      }
      tryAdopt(untrack(deps.virtualItems))
    })
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
