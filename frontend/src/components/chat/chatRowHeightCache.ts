import type { Accessor } from 'solid-js'
import type { VirtualItem } from './useChatVirtualizer'
import { createSignal } from 'solid-js'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { MEASURE_EPSILON_PX } from './createRowMeasurer'

/**
 * One measured row height in transportable form — what `snapshot` exports and
 * `commitAll` re-imports (the reload warm-start round trip, see
 * chatRowHeightPersistence).
 */
export interface PersistableRowHeight {
  id: string
  heightKey: string | undefined
  height: number
}

/**
 * The most recent height commit that changed the offset map -- diagnostic attribution for
 * the `content moved without a scroll` WARN (see chatScrollDiagnostics). A move of the
 * content at the viewport top that no scroll caused follows a geometry change; this
 * identifies the row and the change, so a logged move can be traced to its cause:
 * `previousHeight` is `undefined` for the first measure of a row under its key, and the
 * height before for a measure again (a hidden copy and the row disagree, or the content or
 * the chrome of the row changed). `commitSeq` is monotonic, so two consecutive WARNs reveal
 * how many commits fell between them.
 */
export interface MeasurementCommitInfo {
  id: string
  kind: string
  source: RowHeightSource
  /**
   * The height that the cache answered for the row before this commit (see
   * RowHeightCache.heightOf), or `undefined` when it answered none.
   */
  previousHeight: number | undefined
  newHeight: number
  commitSeq: number
}

/**
 * Max measured-height cache entries. Must stay comfortably ABOVE the largest
 * in-memory window the consumer can pass -- the chat window floats up to its
 * ceiling (MAX_LOADED_CHAT_MESSAGES_CEILING = 8 * 150 = 1200) on a deep scroll
 * through a hidden-message-heavy history, well past the base of 150. Sizing the
 * cache above that ceiling keeps the whole active window cached (mountedIds are
 * additionally protected from eviction), so a measured row is never re-measured
 * while still in the window, while a scroll-through-everything session that churns
 * far more rows than the ceiling still limits memory.
 *
 * Exported so the eviction test derives its loop limit from the real
 * value instead of hard-coding it (and silently breaking when the cap changes).
 */
export const HEIGHT_CACHE_MAX = 2400

/** Reject a height that cannot enter the cumulative offset map. */
function isUsableHeight(n: number): boolean {
  return n >= 0 && Number.isFinite(n)
}

interface CachedMeasuredHeight {
  /**
   * The height key under which a measure produced `height`. Nothing rewrites it: the key
   * holds every input of the height, so the height is a measurement of the row under this
   * key, and it counts again when the row returns to the key.
   */
  key: string | undefined
  height: number
}

/**
 * What a height commit changed: `moved` when it changed the height of the row, `confirmed`
 * when it only put a height that the cache held under another key under the current key,
 * and `none` otherwise. Each change changes `RowHeightCache.version`. A confirmation moves
 * no mounted row, but it changes what `heightOf` answers for a row that is not mounted, and
 * the snapshot that chatRowHeightPersistence saves when the version changes.
 */
export type HeightCommitResult = 'moved' | 'confirmed' | 'none'

/** What read a height: the mounted row itself, or a hidden copy (also a stored height). */
export type RowHeightSource = 'visible' | 'premeasure'

export interface RowHeightCache {
  /** The ids of the mounted rows. The row measurer adds and removes them. */
  mountedIds: Set<string>
  /** Changes with every commit, drop, and eviction that changes an answer of `heightOf`. */
  version: Accessor<number>
  /**
   * The height of the row, or `undefined` for a row that is not measured. Reads `version`.
   *
   * It answers with the cached height when a measure produced it under the current key of
   * the row. For a MOUNTED row it answers with the cached height under any key: the element
   * of the row still shows that height until its next read, which commits the height of the
   * new key (a provisional height). So a mounted row whose key changes keeps its slot, and
   * the rows below it move once, when the content of the row really changes. A row whose
   * last element just detached counts as mounted while its hold lasts (see holdDetached).
   */
  heightOf: (row: Pick<VirtualItem, 'id' | 'heightKey'>) => number | undefined
  /**
   * Whether the cache holds a height that a measure produced under the current key of the
   * row. Not reactive: a caller that needs the change reads `version`.
   */
  isCurrent: (row: Pick<VirtualItem, 'id' | 'heightKey'>) => boolean
  /**
   * Commit a height that was measured under `measuredKey` for `row`, the current
   * item of the row. Refuses a height of another key. A `'premeasure'` commit
   * also refuses a mounted row that has a height. The commit replaces the entry of the
   * row, with `measuredKey` as its key.
   */
  commit: (row: VirtualItem, height: number, measuredKey: string | undefined, source: RowHeightSource) => HeightCommitResult
  /** Commit several premeasured heights with one `version` change. Returns the number adopted. */
  commitAll: (entries: readonly { row: VirtualItem, height: number, measuredKey: string | undefined }[]) => number
  /**
   * Hold the provisional height of the row `id`, whose last element detached, until the
   * returned function runs. While the hold lasts, `heightOf` answers for the row as for a
   * mounted row.
   *
   * The row list replaces the element of a row in one update: the old element detaches
   * before the new one attaches. A reader of the heights between the two steps must still
   * get the height that the row shows, or it keeps the answer "no height" for a row that
   * is mounted again a moment later, and no change of the version corrects it. So the
   * owner of the mounted rows holds the row at the detach. It ends the hold when it knows
   * whether a new element took the row over, and it calls `noteUnmounted` then for a row
   * that no element took over. The end of a hold changes no version by itself.
   */
  holdDetached: (id: string) => () => void
  /**
   * The last element of the row unmounted. `heightOf` stops answering for a row whose
   * cached height is of another key, so the version changes for such a row. It deletes
   * nothing: the height counts again when the row returns to its key.
   */
  noteUnmounted: (row: Pick<VirtualItem, 'id' | 'heightKey'>) => void
  /**
   * The cache as persistable entries, each with the key under which it was measured, in
   * LRU order (least recently measured first) — a consumer that caps the persisted set
   * should keep the TAIL, the most recently measured rows.
   */
  snapshot: () => PersistableRowHeight[]
  /** The cached height of `id` for the raw-JSON debug view, whatever its key. */
  debugEntry: (id: string) => { key: string | undefined, height: number } | undefined
  /** Diagnostic: the most recent commit that changed a height, for WARN attribution. */
  lastMeasurement: () => MeasurementCommitInfo | undefined
}

export interface RowHeightCacheOptions {
  /**
   * The ids of the loaded rows. The eviction keeps their heights, beside those of the
   * mounted rows: a loaded row that lost its height would leave the list until a hidden
   * copy measured it again.
   */
  loadedIds?: () => Iterable<string>
}

/**
 * The measured heights of the chat rows, by row id: for each row, the last height and the
 * key under which a hidden copy or the row measured it. Nothing rewrites that key, and no
 * caller drops an entry, so the cache answers by key (see RowHeightCache.heightOf). It
 * holds no estimate: a row that no measure reached has no height.
 *
 * Survives row unmount so re-entry is flash-free. An LRU cap limits it, so a long session
 * that scrolls through tens of thousands of distinct messages does not grow it without
 * limit (which would defeat the windowing's memory goal). A commit refreshes a row's
 * recency, and the eviction passes over the mounted rows and the loaded rows, so the
 * active window (far smaller than the cap) is never the eviction target.
 */
export function createRowHeightCache(options: RowHeightCacheOptions = {}): RowHeightCache {
  const heightCache = new Map<string, CachedMeasuredHeight>()
  // Ids of the rows currently MOUNTED in the DOM (a keyed <For> attaches one el
  // per id and detaches it on unmount). This is the precise rendered set the
  // eviction must protect -- maintained synchronously by attach/detach rather than
  // derived from the rendered range, which commits on a deferred rAF and
  // could momentarily omit a row that just scrolled in, letting it be evicted the
  // same frame it measured.
  const mountedIds = new Set<string>()
  // Changes whenever an answer of `heightOf` changes, so each reactive reader of a
  // height computes again. The map itself stays plain state for speed.
  const [version, setVersion] = createSignal(0)
  // The number of holds on each row whose last element detached (see holdDetached). A
  // count, because a second detach of a row can come before the first hold ended.
  const detachedHolds = new Map<string, number>()
  // Whether an element shows the cached height of the row, or showed it a moment ago and
  // a new element can still take the row over.
  const showsCachedHeight = (id: string): boolean => mountedIds.has(id) || detachedHolds.has(id)
  // Diagnostic-only: the most recent commit that changed a height, so the
  // `content moved without a scroll` WARN can identify the row that likely caused a move
  // (see MeasurementCommitInfo). Non-reactive -- read at WARN time, never a render input.
  let lastMeasurement: MeasurementCommitInfo | undefined
  let measurementCommitSeq = 0
  const isCurrent = (row: Pick<VirtualItem, 'id' | 'heightKey'>): boolean => {
    const cached = heightCache.get(row.id)
    return cached !== undefined && cached.key === row.heightKey
  }
  // The answer of `heightOf`, with no read of the version: for the code of this unit.
  const cachedMeasuredHeight = (row: Pick<VirtualItem, 'id' | 'heightKey'>): number | undefined => {
    const cached = heightCache.get(row.id)
    if (cached === undefined)
      return undefined
    // A mounted row keeps the height that it shows, whatever its key (a provisional height).
    return cached.key === row.heightKey || showsCachedHeight(row.id)
      ? cached.height
      : undefined
  }
  const heightOf = (row: Pick<VirtualItem, 'id' | 'heightKey'>): number | undefined => {
    version()
    return cachedMeasuredHeight(row)
  }

  // A provisional height describes only the element that showed it. When the last element
  // of the row unmounts, the row is unmeasured under its key again, so each reader of
  // `heightOf` must compute again. The entry stays: its height is a measurement under its
  // own key.
  const noteUnmounted = (row: Pick<VirtualItem, 'id' | 'heightKey'>): void => {
    if (heightCache.has(row.id) && !isCurrent(row))
      setVersion(v => v + 1)
  }

  const holdDetached = (id: string): (() => void) => {
    detachedHolds.set(id, (detachedHolds.get(id) ?? 0) + 1)
    let held = true
    return () => {
      // A second call changes nothing: the hold ended once.
      if (!held)
        return
      held = false
      const count = detachedHolds.get(id) ?? 0
      if (count <= 1)
        detachedHolds.delete(id)
      else
        detachedHolds.set(id, count - 1)
    }
  }

  /**
   * The commit core shared by the single-row path (which changes `version` per
   * commit) and the bulk path (which changes it once per batch): validates, updates
   * the cache and runs the eviction, and records the commit diagnostic.
   * Returns what the commit changed (the caller owes a `version` change for any change).
   */
  const applyHeightCommit = (
    row: VirtualItem,
    height: number,
    measuredKey: string | undefined,
    source: RowHeightSource,
  ): HeightCommitResult => {
    const { id } = row
    // DOM callers verify layout before this numeric commit. A valid empty row
    // has a zero height.
    if (!isUsableHeight(height))
      return 'none'
    if (measuredKey !== row.heightKey)
      return 'none'
    const prevEntry = heightCache.get(id)
    // The height that the cache answered for the row before this commit: its difference
    // from the new height is what shifts the offset map (the layout shift then holds the
    // view).
    const previousHeight = cachedMeasuredHeight(row)
    // A measure within epsilon of the height of the entry is noise: the height of the row
    // doesn't change. Still refresh the row's LRU recency (delete + re-set moves it to the
    // most-recently-used end) so a row that stays mounted and measures again at a stable
    // height can't age to the eviction front. A height that the cache held under another
    // key is now a height of the current key: the measure confirmed it.
    if (prevEntry !== undefined && Math.abs(prevEntry.height - height) < MEASURE_EPSILON_PX) {
      heightCache.delete(id)
      heightCache.set(id, { key: measuredKey, height: prevEntry.height })
      return prevEntry.key !== measuredKey
        ? 'confirmed'
        : 'none'
    }
    // Re-insert so this row becomes the most-recently-used entry (Map preserves
    // insertion order; a plain set on an existing key would keep its old, stale
    // position and risk evicting a freshly-measured row).
    heightCache.delete(id)
    heightCache.set(id, { key: measuredKey, height })
    // Evict the least-recently-measured rows once over the cap, and never drop a row that
    // is mounted or loaded: such a row would lose its place in the list until a hidden
    // copy measured it again. The union is built only when an eviction runs.
    if (heightCache.size > HEIGHT_CACHE_MAX) {
      const loadedIds = options.loadedIds?.()
      const protect = loadedIds === undefined
        ? mountedIds
        : new Set([
            ...mountedIds,
            ...loadedIds,
          ])
      capMapInsertionOrder(heightCache, HEIGHT_CACHE_MAX, { protect })
    }
    // Record the commit that is about to move the offset map, so a WARN about a move of
    // the content at the viewport top can identify its likely cause (see MeasurementCommitInfo).
    measurementCommitSeq += 1
    lastMeasurement = {
      id,
      kind: row.kind ?? '',
      source,
      previousHeight,
      newHeight: height,
      commitSeq: measurementCommitSeq,
    }
    return 'moved'
  }

  // Once a visible row has committed a measurement, the live DOM is the authoritative
  // geometry. A hidden premeasure queued before mount can finish later; it must not
  // replace the visible height with its hidden-layout read.
  const refusesPremeasure = (row: VirtualItem): boolean =>
    showsCachedHeight(row.id) && cachedMeasuredHeight(row) !== undefined

  const commit = (
    row: VirtualItem,
    height: number,
    measuredKey: string | undefined,
    source: RowHeightSource,
  ): HeightCommitResult => {
    if (source === 'premeasure' && refusesPremeasure(row))
      return 'none'
    const result = applyHeightCommit(row, height, measuredKey, source)
    if (result !== 'none')
      setVersion(v => v + 1)
    return result
  }

  const commitAll = (entries: readonly { row: VirtualItem, height: number, measuredKey: string | undefined }[]): number => {
    let adopted = 0
    let changed = false
    for (const entry of entries) {
      if (refusesPremeasure(entry.row))
        continue
      const result = applyHeightCommit(entry.row, entry.height, entry.measuredKey, 'premeasure')
      if (result === 'moved')
        adopted += 1
      if (result !== 'none')
        changed = true
    }
    if (changed)
      setVersion(v => v + 1)
    return adopted
  }

  const snapshot = (): PersistableRowHeight[] => {
    const out: PersistableRowHeight[] = []
    for (const [id, cached] of heightCache)
      out.push({ id, heightKey: cached.key, height: cached.height })
    return out
  }

  const debugEntry = (id: string): { key: string | undefined, height: number } | undefined => {
    const cached = heightCache.get(id)
    return cached === undefined
      ? undefined
      : { key: cached.key, height: cached.height }
  }

  return {
    mountedIds,
    version,
    heightOf,
    isCurrent,
    commit,
    commitAll,
    holdDetached,
    noteUnmounted,
    snapshot,
    debugEntry,
    lastMeasurement: () => lastMeasurement,
  }
}
