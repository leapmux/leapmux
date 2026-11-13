import type { Accessor, Setter } from 'solid-js'
import { createEffect, createMemo, createSignal, getListener, on, onCleanup, untrack } from 'solid-js'
import { setWith, setWithout } from '~/lib/immutableCollections'

// ---------------------------------------------------------------------------
// Render readiness
//
// An asynchronous body can mount before its content exists: a Markdown body
// renders its HTML in the Markdown worker, an edit diff waits for the diff
// worker, and a diff view mounts its lines in idle slices. The row that holds
// such a body must neither reveal nor commit a height until the body draws its
// final content: a height measured without the content is wrong, and committing
// it shifts every row below when the content lands.
//
// A body reports its pending state to the readiness scope of the row copy that
// mounts it. The scope answers one question: does this copy still wait for
// content? The hidden premeasure copy and the visible copy of a row each have a
// scope of their own, because each mounts its own bodies and measures its own
// element. ChatView collects the visible copies' scopes in a registry keyed by
// row id, because the skeleton overlay and the reveal order live outside the rows.
//
// A body can also replace content that it shows: a diff view that switches
// between unified and split keeps the old layout in view while the new one
// mounts behind it. The copy shows complete content then, so it stays visible,
// but its height is about to change, so a reader that measures it waits. The
// body reports that state as `replacing`, apart from `pending`.
// ---------------------------------------------------------------------------

/** Where an asynchronous body reports that it has not drawn its final content yet. */
export interface RenderReadiness {
  /**
   * Report a body's pending state for the life of the caller's reactive owner.
   * Call it once, while the body sets up: the report ends when that owner is
   * disposed, so an unmounted body never holds its row.
   */
  track: (pending: Accessor<boolean>) => void
  /**
   * Report, for the life of the caller's reactive owner, while a body mounts new
   * content behind the content that it shows, until it shows the new content. The
   * copy stays visible, but a reader that measures it waits until the body shows
   * the new content, because the height of the copy is about to change. Call it
   * once, while the body sets up.
   */
  trackReplacing: (replacing: Accessor<boolean>) => void
}

/**
 * Report an asynchronous body's pending state to the readiness scope that its
 * render context carries. Call it once, while the body sets up.
 *
 * Returns true when the context carries no scope: the body mounts outside a chat
 * row, so it must draw a skeleton of its own while it waits. Inside a chat row it
 * draws nothing while it waits, because the row stays hidden behind its own
 * skeleton, with its height uncommitted, until every body is ready.
 */
export function useRenderReadiness(
  context: () => { readiness?: RenderReadiness | undefined } | undefined,
  pending: Accessor<boolean>,
): boolean {
  const readiness = untrack(() => context()?.readiness)
  readiness?.track(pending)
  return readiness === undefined
}

/**
 * Report to the readiness scope that a body's render context carries while the
 * body replaces the content that it shows (see RenderReadiness.trackReplacing).
 * Call it once, while the body sets up. Outside a readiness scope it reports
 * nothing: no reader waits.
 */
export function useReplacingReadiness(
  context: () => { readiness?: RenderReadiness | undefined } | undefined,
  replacing: Accessor<boolean>,
): void {
  untrack(() => context()?.readiness)?.trackReplacing(replacing)
}

/** The readiness of one row copy: what its bodies report, and the answer. */
export interface RenderReadinessScope extends RenderReadiness {
  /** True while any body that this copy mounts is pending. */
  pending: Accessor<boolean>
  /** True while any body that this copy mounts replaces the content that it shows (see trackReplacing). */
  replacing: Accessor<boolean>
  /** True while no body of this copy is pending or replaces its content: the height of the copy is final. */
  measurable: Accessor<boolean>
  /**
   * Run `callback` each time the copy turns measurable after it was not: the
   * moment to measure the copy again, because its content landed, or a body
   * shows the content that replaced the old. A copy that is measurable from the
   * start never runs it. Call within the reactive owner that measures the copy:
   * the subscription ends when that owner is disposed.
   */
  onMeasurable: (callback: () => void) => void
}

/** The reports of one kind that the bodies of a copy made, and whether any of them is true. */
interface ReadinessReports {
  any: Accessor<boolean>
  /** Add a report for the life of the caller's reactive owner. */
  add: (report: Accessor<boolean>) => void
}

function createReadinessReports(): ReadinessReports {
  const [reports, setReports] = createSignal<ReadonlySet<Accessor<boolean>>>(new Set())
  return {
    any: createMemo(() => {
      for (const report of reports()) {
        if (report())
          return true
      }
      return false
    }),
    add(report) {
      setReports(prev => setWith(prev, report))
      onCleanup(() => setReports(prev => setWithout(prev, report)))
    },
  }
}

/**
 * Create the readiness scope of one row copy. Call within the copy's reactive
 * owner: the memos of the scope belong to it.
 */
export function createRenderReadinessScope(): RenderReadinessScope {
  const pendingReports = createReadinessReports()
  const replacingReports = createReadinessReports()
  const measurable = createMemo(() => !pendingReports.any() && !replacingReports.any())
  return {
    pending: pendingReports.any,
    replacing: replacingReports.any,
    measurable,
    track: pendingReports.add,
    trackReplacing: replacingReports.add,
    onMeasurable(callback) {
      createEffect(on(measurable, (isMeasurable) => {
        if (isMeasurable)
          callback()
      }, { defer: true }))
    },
  }
}

/** The readiness of every visible row copy, keyed by row id. */
export interface RowReadinessRegistry {
  /**
   * Create the readiness scope of one visible row copy and register it under
   * `id`. Call within the row's reactive owner (the `<For>` callback): the
   * registration ends when the row unmounts.
   */
  trackRow: (id: string) => RenderReadinessScope
  /**
   * Whether row `id` has a mounted copy. Reactive. A row in the rendered slice
   * that has not mounted yet mounts later in the same update, so a derivation
   * that builds something per row can skip it until then.
   */
  isMounted: (id: string) => boolean
  /**
   * Whether row `id` has a mounted copy and no copy holds a pending body. Reactive.
   *
   * A row that has not mounted yet is NOT ready. A row in the rendered slice mounts
   * in the same update that adds it, but derivations over the slice run before the
   * `<For>` mounts it, and its bodies report only once they mount. The ordered tail
   * reveal is one such derivation: had it seen an unmounted row as ready, it would
   * have released a later row in front of that row, and a release is permanent.
   */
  isReady: (id: string) => boolean
}

/** The mounted copies of one row id, and the computations that read their readiness. */
interface RowCopies {
  /** The scopes of the mounted copies. Each change replaces the set, so the signal notifies. */
  scopes: Accessor<ReadonlySet<RenderReadinessScope>>
  setScopes: Setter<ReadonlySet<RenderReadinessScope>>
  /** How many reads by a computation are still active: each ends when its reader re-runs or is disposed. */
  readers: number
}

const NO_SCOPES: ReadonlySet<RenderReadinessScope> = new Set()

function scopesReady(scopes: ReadonlySet<RenderReadinessScope>): boolean {
  if (scopes.size === 0)
    return false
  for (const scope of scopes) {
    if (scope.pending())
      return false
  }
  return true
}

/**
 * Create the registry.
 *
 * Each row id has its own signal, so a mount, an unmount or a body report
 * notifies only the computations that read that row. One signal shared by every
 * row would re-run every mounted row's style effect each time any row enters or
 * leaves the rendered slice. An id's entry exists while a copy is mounted or a
 * computation reads it, so the registry holds no entry for a row that left.
 *
 * Keyed by SCOPE within an id: a row can remount under a new element before the
 * old one's cleanup runs, and the old scope's cleanup must not erase the new
 * scope's state.
 *
 * PULL-based: a read calls each scope's `pending` memo directly, so a read is
 * current the moment a body reports. A computation that copied each scope's
 * state into a signal would run only at the end of the update, and the next row
 * mounted in the same update would read a ready state its predecessor had
 * already given up.
 */
export function createRowReadinessRegistry(): RowReadinessRegistry {
  const rows = new Map<string, RowCopies>()
  const rowCopies = (id: string): RowCopies => {
    let row = rows.get(id)
    if (row === undefined) {
      const [scopes, setScopes] = createSignal(NO_SCOPES)
      row = { scopes, setScopes, readers: 0 }
      rows.set(id, row)
    }
    return row
  }
  // A signal that no computation reads and that holds no copy carries no state.
  const forgetIfUnused = (id: string, row: RowCopies): void => {
    if (row.readers === 0 && untrack(row.scopes).size === 0 && rows.get(id) === row)
      rows.delete(id)
  }
  // The scopes of row `id`'s mounted copies, read reactively.
  const readScopes = (id: string): ReadonlySet<RenderReadinessScope> => {
    // An untracked read subscribes nothing, so it needs no entry of its own.
    if (getListener() === null)
      return rows.get(id)?.scopes() ?? NO_SCOPES
    // The reader's own cleanup ends this read, as Solid's createSelector does.
    const row = rowCopies(id)
    row.readers += 1
    onCleanup(() => {
      row.readers -= 1
      forgetIfUnused(id, row)
    })
    return row.scopes()
  }
  return {
    trackRow(id) {
      const scope = createRenderReadinessScope()
      const row = rowCopies(id)
      row.setScopes(prev => setWith(prev, scope))
      onCleanup(() => {
        row.setScopes(prev => setWithout(prev, scope))
        forgetIfUnused(id, row)
      })
      return scope
    },
    isMounted: id => readScopes(id).size > 0,
    isReady: id => scopesReady(readScopes(id)),
  }
}
