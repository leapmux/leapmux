import type { Projection } from './project'
import type { UserCrdtState } from '~/generated/leapmux/v1/user_crdt_pb'
import { createMemo } from 'solid-js'
import { project, ProjectionCache } from './project'

/**
 * A `Projection` memo over a CRDT-state accessor, with the cache it needs.
 *
 * Owns the whole `ProjectionCache` lifecycle, because half of it is not
 * expressible from the cache alone: `begin` evicts by generation, so eviction
 * only ever happens on a call to `project()`, and a caller that STOPS projecting
 * would pin the last tenant's entire graph -- every `RenderTree`, `RenderedTab`
 * and `RenderedFloatingWindow`, plus every `LayoutNodeLocal` those trees still
 * key in `renderTreeToLocal`'s cache. One factory owning both halves is what
 * keeps a second caller from wiring up the memo and forgetting the `clear()`.
 *
 * The `{ equals: false }` on the state accessor is the CALLER's, deliberately:
 * production derives it from `pendingVersion()` + `pendingMgr()` and the test
 * harness from `getCRDTBridge()?.speculativeState()`. What must NOT vary between
 * them is everything below.
 */
export function createProjectionMemo(state: () => UserCrdtState | null): () => Projection | null {
  const cache = new ProjectionCache()
  return createMemo(() => {
    const s = state()
    if (!s) {
      cache.clear()
      return null
    }
    return project(s, cache)
  })
}
