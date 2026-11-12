import type { Accessor } from 'solid-js'
import { createMemo, createSignal, onCleanup } from 'solid-js'
import { mapWith, mapWithout, setWith, setWithout } from '~/lib/immutableCollections'
import { shallowEqualSets } from '~/lib/shallowEqual'

// ---------------------------------------------------------------------------
// Render readiness
//
// A Markdown body renders in a worker, so it can mount before its HTML exists.
// The row that holds it must neither reveal nor commit a height until the body
// draws its final content: a height measured without the content is wrong, and
// committing it shifts every row below when the content lands.
//
// A body reports its pending state to the readiness scope of the row copy that
// mounts it. The scope answers one question: does this copy still wait for
// content? The hidden premeasure copy and the visible copy of a row each have a
// scope of their own, because each mounts its own bodies and measures its own
// element. ChatView collects the visible copies' scopes in a registry keyed by
// row id, because the skeleton overlay and the reveal order live outside the rows.
// ---------------------------------------------------------------------------

/** Where an asynchronous body reports that it has not drawn its final content yet. */
export interface RenderReadiness {
  /**
   * Report a body's pending state for the life of the caller's reactive owner.
   * Call it once, while the body sets up: the report ends when that owner is
   * disposed, so an unmounted body never holds its row.
   */
  track: (pending: Accessor<boolean>) => void
}

/** The readiness of one row copy: what its bodies report, and the answer. */
export interface RenderReadinessScope extends RenderReadiness {
  /** True while any body that this copy mounts is pending. */
  pending: Accessor<boolean>
}

/**
 * Create the readiness scope of one row copy. Call within the copy's reactive
 * owner: the `pending` memo belongs to it.
 */
export function createRenderReadinessScope(): RenderReadinessScope {
  const [sources, setSources] = createSignal<ReadonlySet<Accessor<boolean>>>(new Set())
  const pending = createMemo(() => {
    for (const source of sources()) {
      if (source())
        return true
    }
    return false
  })
  return {
    pending,
    track(source) {
      setSources(prev => setWith(prev, source))
      onCleanup(() => setSources(prev => setWithout(prev, source)))
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

/**
 * Create the registry. Keyed by SCOPE, not by id: a row can remount under a new
 * element before the old one's cleanup runs, and the old scope's cleanup must
 * not erase the new scope's state.
 *
 * PULL-based: the memo reads each scope's `pending` directly, so a read is current
 * the moment a body reports. A computation that copied each scope's state into a
 * signal would run only at the end of the update, and the next row mounted in the
 * same update would read a ready state its predecessor had already given up.
 */
export function createRowReadinessRegistry(): RowReadinessRegistry {
  const [copies, setCopies] = createSignal<ReadonlyMap<RenderReadinessScope, string>>(new Map())
  const readyIds = createMemo<ReadonlySet<string>>(
    () => {
      const mounted = new Set<string>()
      const pending = new Set<string>()
      for (const [scope, id] of copies()) {
        mounted.add(id)
        if (scope.pending())
          pending.add(id)
      }
      return new Set([...mounted].filter(id => !pending.has(id)))
    },
    new Set(),
    { equals: shallowEqualSets },
  )
  return {
    trackRow(id) {
      const scope = createRenderReadinessScope()
      setCopies(prev => mapWith(prev, scope, id))
      onCleanup(() => setCopies(prev => mapWithout(prev, scope)))
      return scope
    },
    isReady: id => readyIds().has(id),
  }
}
