import type { Accessor } from 'solid-js'
import type { ChatRowItem } from './chatRowItem'
import { createSignal } from 'solid-js'
import { capMapInsertionOrder } from '~/lib/mapLru'
import { MEASURE_EPSILON_PX } from './createRowMeasurer'

/**
 * A row as the cache identifies it: its id, and its current height key.
 */
export type HeightKeyedRow = Pick<ChatRowItem, 'id' | 'heightKey'>

/**
 * One measured row height in transportable form, with no live row: what `snapshot`
 * exports for the reload warm start. chatRowHeightPersistence stores a digest of the key,
 * and after a reload it adopts each stored height whose digest matches the key of the
 * loaded row, through `commitAll` with that row.
 */
export interface PersistableRowHeight {
  id: string
  heightKey: string | undefined
  height: number
}

/**
 * The most recent commit that changed the height of a row -- diagnostic attribution for
 * the `content moved without a scroll` WARN (see chatScrollDiagnostics). A move of the
 * content at the viewport top that no scroll caused follows a geometry change; this
 * identifies a row and a change, so a logged move can be traced to its likely cause.
 *
 * The record covers every loaded row, not only the rows of the list: the commit of a
 * hidden copy for a staged row is recorded too, and such a commit moves the offset map
 * only when it lets the row join the list. A commit that only confirms a height under a
 * new key (`'confirmed'`) is not recorded, although it can let a row join the list.
 *
 * `previousHeight` is the height that the cache answered for the row before the commit:
 * `undefined` for a row with no answer, the height before for a measure again (a hidden
 * copy and the row disagree, or the content or the chrome of the row changed), and for a
 * mounted row also a provisional height of another key. `commitSeq` is monotonic, so two
 * consecutive WARNs reveal how many commits fell between them.
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
 * Max measured-height cache entries. The eviction never drops the height of a loaded row
 * or of a mounted row (see RowHeightCacheOptions.loadedIds), so this cap limits only the
 * heights of the rows that left the loaded window: a scroll-through-everything session
 * that churns far more rows than the window holds still limits memory, and a row that
 * returns to the window soon needs no new measure. The cap stays above the ceiling of the
 * loaded window (MAX_LOADED_CHAT_MESSAGES_CEILING = 8 * 150 = 1200), so that it leaves
 * room for such rows beside a full window.
 *
 * Exported so the eviction test derives its loop limit from the real
 * value instead of hard-coding it (and silently breaking when the cap changes).
 */
export const HEIGHT_CACHE_MAX = 2400

/**
 * The number of entries below the cap that one eviction frees. An eviction builds the set
 * of the rows that it must keep (every mounted and every loaded row) and walks past their
 * entries. A cache at the cap would do that for each commit of a new row: a page of 150
 * newly measured rows would build 150 sets of up to 1200 ids. So one eviction drops this
 * many of the least recently measured entries, and the next ones need no eviction.
 */
export const HEIGHT_CACHE_EVICTION_BATCH = 240

/** Reject a height that cannot enter the cumulative offset map. */
function isUsableHeight(n: number): boolean {
  return n >= 0 && Number.isFinite(n)
}

export interface CachedMeasuredHeight {
  /**
   * The height key of the last measure that produced `height`, or that confirmed it within
   * MEASURE_EPSILON_PX. Only a commit under a key sets it: the key holds every input of
   * the height, so the height is a measurement of the row under this key, and it counts
   * again when the row returns to the key.
   */
  key: string | undefined
  height: number
}

/**
 * One height for `commitAll`: the row, and a height that a measure produced under the
 * current key of the row. The caller checks the key, as chatRowHeightPersistence does
 * with the digest of the stored key.
 */
export interface RowHeightCommitEntry {
  row: ChatRowItem
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
  /**
   * The ids of the rows that have an attached element, as `noteMounted` and `noteDetached`
   * record them. A live view: read it at use time. The eviction keeps the heights of these
   * rows, and a sibling cache (the UI state of each message) protects the same set.
   */
  mountedIds: ReadonlySet<string>
  /**
   * Whether the row counts as mounted: an element of it is attached, or its last element
   * detached and the microtask of that detach did not run yet (see noteDetached). Not
   * reactive.
   */
  isMounted: (id: string) => boolean
  /**
   * Changes with every commit that changes an answer of `heightOf`, and with the unmount
   * of a row for which `heightOf` answered a provisional height (see noteDetached). An
   * eviction runs inside a commit.
   */
  version: Accessor<number>
  /**
   * The height of the row, or `undefined` for a row that is not measured. Reads `version`.
   *
   * It answers with the cached height when a measure produced it under the current key of
   * the row. For a MOUNTED row it answers with the cached height under any key: the element
   * of the row still shows that height until its next read, which commits the height of the
   * new key (a provisional height). So a mounted row whose key changes keeps its slot, and
   * the rows below it move once, when the content of the row really changes. A row whose
   * last element just detached counts as mounted for one microtask (see noteDetached).
   */
  heightOf: (row: HeightKeyedRow) => number | undefined
  /**
   * Whether the row is measured: `heightOf` answers for it. So a mounted row with a
   * provisional height counts as measured. Reads `version`.
   */
  isMeasured: (row: HeightKeyedRow) => boolean
  /**
   * The height that a measure produced under the current key of the row, or `undefined`.
   * It differs from `heightOf` for a mounted row whose key changed: such a row has no
   * height under its current key until its next read. Not reactive: a caller that needs
   * the change reads `version`.
   */
  heightUnderCurrentKey: (row: HeightKeyedRow) => number | undefined
  /**
   * Whether `heightOf` answers a provisional height for the row: the cache holds a height
   * of another key, and the row counts as mounted. Not reactive. It records no answer, so
   * the unmount of the row changes no version for this question (see noteDetached).
   */
  hasProvisionalHeight: (row: HeightKeyedRow) => boolean
  /**
   * Commit a height that was measured under `measuredKey` for `row`, the current
   * item of the row. Refuses a height of another key. A `'premeasure'` commit
   * also refuses a mounted row that has a height. The commit replaces the entry of the
   * row, with `measuredKey` as its key.
   */
  commit: (row: ChatRowItem, height: number, measuredKey: string | undefined, source: RowHeightSource) => HeightCommitResult
  /**
   * Commit several premeasured heights, each under the current key of its row, with one
   * `version` change. Returns the number adopted.
   */
  commitAll: (entries: readonly RowHeightCommitEntry[]) => number
  /** An element of the row `id` attached: the row is mounted. */
  noteMounted: (id: string) => void
  /**
   * The last element of the row `id` detached. The id leaves `mountedIds` at once.
   *
   * For one microtask, the row still counts as mounted (see isMounted), and `heightOf`
   * still answers for it as for a mounted row. The row list replaces the element of a row
   * in one update: the old element detaches before the new one attaches. A reader of the
   * heights between the two steps must still get the height that the row shows, or it
   * keeps the answer "no height" for a row that is mounted again a moment later, and no
   * change of the version corrects it.
   *
   * After that microtask, a row that no element took over is unmounted. `heightOf` then
   * stops answering a provisional height for it, so the version changes when a reader got
   * such an answer for the row. That holds also for a row that left the list of the
   * virtualizer: the row is still loaded, and the admission and the staging bands ask for
   * it. The cache deletes nothing: the height counts again when the row returns to its
   * key. `unmounted` then runs, so that the caller can drop what it recorded for the
   * mounted row. It does not run for a row that an element took over.
   */
  noteDetached: (id: string, unmounted?: () => void) => void
  /**
   * The cache as persistable entries, each with the key under which it was measured, in
   * LRU order (least recently measured first) — a consumer that caps the persisted set
   * should keep the TAIL, the most recently measured rows.
   */
  snapshot: () => PersistableRowHeight[]
  /** The cached height of `id` for the raw-JSON debug view, whatever its key. */
  debugEntry: (id: string) => CachedMeasuredHeight | undefined
  /** Diagnostic: the most recent commit that changed a height, for WARN attribution. */
  lastMeasurement: () => MeasurementCommitInfo | undefined
}

export interface RowHeightCacheOptions {
  /**
   * The ids of the loaded rows. The eviction keeps their heights, beside those of the
   * mounted rows: a loaded row that lost its height would leave the list until a hidden
   * copy measured it again.
   */
  loadedIds: () => Iterable<string>
}

/**
 * The measured heights of the chat rows, by row id: for each row, the last height and the
 * key under which a hidden copy or the row measured it. Only a commit changes that key,
 * and no caller drops an entry, so the cache answers by key (see RowHeightCache.heightOf).
 * It holds no estimate: a row that no measure reached has no height.
 *
 * Survives row unmount so re-entry is flash-free. An LRU cap limits it, so a long session
 * that scrolls through tens of thousands of distinct messages does not grow it without
 * limit (which would defeat the windowing's memory goal). A commit refreshes a row's
 * recency, and the eviction passes over the mounted rows and the loaded rows, so the
 * active window is never the eviction target.
 */
export function createRowHeightCache(options: RowHeightCacheOptions): RowHeightCache {
  const heightCache = new Map<string, CachedMeasuredHeight>()
  // Ids of the rows currently MOUNTED in the DOM (a keyed <For> attaches one el
  // per id and detaches it on unmount). This is the precise rendered set the
  // eviction must protect -- maintained synchronously at the attach and the detach
  // (see noteMounted and noteDetached) rather than derived from the rendered range,
  // which commits on a deferred rAF and could momentarily omit a row that just
  // scrolled in, letting it be evicted the same frame it measured.
  const mountedIds = new Set<string>()
  // Changes whenever an answer of `heightOf` changes, so each reactive reader of a
  // height computes again. The map itself stays plain state for speed.
  const [version, setVersion] = createSignal(0)
  // The number of holds on each row whose last element detached (see noteDetached). A
  // count, because a second detach of a row can come before the first hold ended.
  const detachedHolds = new Map<string, number>()
  // The rows for which `heightOf` answered a provisional height since their last commit.
  // The unmount of such a row changes an answer that a reader holds, so it must change
  // the version. The set needs no key of a row: it records the answers that were given.
  const provisionalAnswers = new Set<string>()
  // Whether an element shows the cached height of the row, or showed it a moment ago and
  // a new element can still take the row over (see RowHeightCache.isMounted).
  const isMounted = (id: string): boolean => mountedIds.has(id) || detachedHolds.has(id)
  // Diagnostic-only: the most recent commit that changed a height, so the
  // `content moved without a scroll` WARN can identify the row that likely caused a move
  // (see MeasurementCommitInfo). Non-reactive -- read at WARN time, never a render input.
  let lastMeasurement: MeasurementCommitInfo | undefined
  let measurementCommitSeq = 0
  const heightUnderCurrentKey = (row: HeightKeyedRow): number | undefined => {
    const cached = heightCache.get(row.id)
    return cached !== undefined && cached.key === row.heightKey
      ? cached.height
      : undefined
  }
  // Whether the cache answers a provisional height for the row: the height of another key,
  // which an element of the row still shows.
  const answersProvisionally = (row: HeightKeyedRow, cached: CachedMeasuredHeight): boolean =>
    cached.key !== row.heightKey && isMounted(row.id)
  // The answer of `heightOf`, with no read of the version and no record of the answer:
  // for the code of this unit.
  const cachedMeasuredHeight = (row: HeightKeyedRow): number | undefined => {
    const cached = heightCache.get(row.id)
    if (cached === undefined)
      return undefined
    // A mounted row keeps the height that it shows, whatever its key (a provisional height).
    return cached.key === row.heightKey || answersProvisionally(row, cached)
      ? cached.height
      : undefined
  }
  const hasProvisionalHeight = (row: HeightKeyedRow): boolean => {
    const cached = heightCache.get(row.id)
    return cached !== undefined && answersProvisionally(row, cached)
  }
  const heightOf = (row: HeightKeyedRow): number | undefined => {
    version()
    const cached = heightCache.get(row.id)
    if (cached === undefined)
      return undefined
    if (cached.key === row.heightKey)
      return cached.height
    if (!answersProvisionally(row, cached))
      return undefined
    provisionalAnswers.add(row.id)
    return cached.height
  }
  const isMeasured = (row: HeightKeyedRow): boolean => heightOf(row) !== undefined

  // A provisional height describes only the element that showed it. When the last element
  // of the row unmounts, the row is unmeasured under its key again, so each reader that got
  // the provisional height must compute again. The entry stays: its height is a measurement
  // under its own key.
  const noteDetached = (id: string, unmounted?: () => void): void => {
    mountedIds.delete(id)
    detachedHolds.set(id, (detachedHolds.get(id) ?? 0) + 1)
    queueMicrotask(() => {
      const count = detachedHolds.get(id) ?? 0
      if (count <= 1)
        detachedHolds.delete(id)
      else
        detachedHolds.set(id, count - 1)
      // A new element took the row over, or a later detach still holds it: every answer
      // for the row stands.
      if (isMounted(id))
        return
      if (provisionalAnswers.delete(id))
        setVersion(v => v + 1)
      unmounted?.()
    })
  }

  /**
   * The commit core shared by the single-row path (which changes `version` per
   * commit) and the bulk path (which changes it once per batch): validates, updates
   * the cache and runs the eviction, and records the commit diagnostic.
   * Returns what the commit changed (the caller owes a `version` change for any change).
   */
  const applyHeightCommit = (
    row: ChatRowItem,
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
      // The entry is of the current key now, so no answer for the row is provisional.
      provisionalAnswers.delete(id)
      return prevEntry.key !== measuredKey
        ? 'confirmed'
        : 'none'
    }
    // Re-insert so this row becomes the most-recently-used entry (Map preserves
    // insertion order; a plain set on an existing key would keep its old, stale
    // position and risk evicting a freshly-measured row).
    heightCache.delete(id)
    heightCache.set(id, { key: measuredKey, height })
    provisionalAnswers.delete(id)
    // Evict the least-recently-measured rows once over the cap, and never drop a row that
    // is mounted or loaded: such a row would lose its place in the list until a hidden
    // copy measured it again. The union is built only when an eviction runs, and one
    // eviction frees room for the next commits (see HEIGHT_CACHE_EVICTION_BATCH).
    if (heightCache.size > HEIGHT_CACHE_MAX) {
      const protect = new Set([
        ...mountedIds,
        ...options.loadedIds(),
      ])
      capMapInsertionOrder(heightCache, HEIGHT_CACHE_MAX - HEIGHT_CACHE_EVICTION_BATCH, { protect })
    }
    // Record the commit that changed the height of the row, so a WARN about a move of the
    // content at the viewport top can identify its likely cause (see MeasurementCommitInfo).
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
  const refusesPremeasure = (row: ChatRowItem): boolean =>
    isMounted(row.id) && cachedMeasuredHeight(row) !== undefined

  const commit = (
    row: ChatRowItem,
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

  const commitAll = (entries: readonly RowHeightCommitEntry[]): number => {
    let adopted = 0
    let changed = false
    for (const entry of entries) {
      if (refusesPremeasure(entry.row))
        continue
      const result = applyHeightCommit(entry.row, entry.height, entry.row.heightKey, 'premeasure')
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

  const debugEntry = (id: string): CachedMeasuredHeight | undefined => {
    const cached = heightCache.get(id)
    return cached === undefined
      ? undefined
      : { key: cached.key, height: cached.height }
  }

  return {
    mountedIds,
    isMounted,
    version,
    heightOf,
    isMeasured,
    heightUnderCurrentKey,
    hasProvisionalHeight,
    commit,
    commitAll,
    noteMounted: (id) => {
      mountedIds.add(id)
    },
    noteDetached,
    snapshot,
    debugEntry,
    lastMeasurement: () => lastMeasurement,
  }
}
