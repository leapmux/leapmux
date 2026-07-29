import type { TabMetadataStore } from '~/stores/tabMetadata.store'
import type { TabSelectionStore } from '~/stores/tabSelection.store'
import type { TabView } from '~/stores/tabView'
import { createMemo } from 'solid-js'
import { useFocusInvariant } from '~/components/shell/useFocusInvariant'
import { getCRDTBridge, project } from '~/lib/crdt'
import { createFloatingWindowStore } from '~/stores/floatingWindow.store'
import { createLayoutStore } from '~/stores/layout.store'
import { createTabMetadataStore } from '~/stores/tabMetadata.store'
import { createTabSelectionStore } from '~/stores/tabSelection.store'
import { createTabView } from '~/stores/tabView'

/**
 * The store bundle AppShell builds, for tests.
 *
 * Mirrors the production wiring in one place so a change to how the projection
 * feeds the stores doesn't have to be re-applied across every spec. Call inside
 * `withTestBridge` + `createRoot`.
 *
 * `equals: false` on the state accessor is load-bearing and easy to lose:
 * `PendingOpsManager` mutates `speculativeState` IN PLACE, so its identity never
 * changes and a default memo swallows every update after the first.
 */
export interface TestTabStores {
  view: TabView
  metadata: TabMetadataStore
  selection: TabSelectionStore
  layoutStore: ReturnType<typeof createLayoutStore>
  floatingWindowStore: ReturnType<typeof createFloatingWindowStore>
  projection: () => ReturnType<typeof project> | null
}

/**
 * The state + projection memo pair, in ONE place.
 *
 * Every factory below needs it, and the `equals: false` above is exactly the
 * kind of detail that survives in one copy and quietly not the other. Exported
 * because specs that build a single store directly need the same wiring.
 */
export function projectionMemo() {
  const bridge = getCRDTBridge()
  const state = createMemo(() => bridge?.speculativeState() ?? null, undefined, { equals: false })
  const projection = createMemo(() => {
    const s = state()
    return s ? project(s) : null
  })
  return { state, projection }
}

export function createTestTabStores(workspaceId: string): TestTabStores {
  const { state, projection } = projectionMemo()
  const metadata = createTabMetadataStore()
  const view = createTabView({ projection, state, metadata })
  const selection = createTabSelectionStore(view, metadata)
  const layoutStore = createLayoutStore({ getWorkspaceId: () => workspaceId, projection })
  const floatingWindowStore = createFloatingWindowStore({ getWorkspaceId: () => workspaceId, projection })
  // Part of the production wiring, and easy to leave out because it returns
  // nothing: `layout.store` deliberately does NOT enforce the focus invariant
  // itself (see its own note), so without this the double is the one
  // configuration in which a focused tile can survive its own tile tree. A spec
  // that closes a tile, merges into an heir, or drops a floating window would
  // then assert on a focus value the real shell repairs.
  useFocusInvariant({ layoutStore, floatingWindowStore })
  return { view, metadata, selection, layoutStore, floatingWindowStore, projection }
}

/**
 * A layout store wired to the installed test bridge's workspace.
 *
 * Layout tests drive the store through real ops against `withTestBridge`, whose
 * seeded workspace id is `ws-test` unless overridden — so the store only needs
 * to know which workspace to project, and reads its tree from the same shared
 * projection AppShell uses.
 */
export function createTestLayoutStore(workspaceId = 'ws-test') {
  const { projection } = projectionMemo()
  return createLayoutStore({ getWorkspaceId: () => workspaceId, projection })
}

/**
 * A floating-window store wired the way production wires it, from the installed
 * test bridge's own workspace.
 *
 * The store derives its windows from the shared projection rather than walking
 * the raw state itself, so a spec that built it with an ad-hoc projection would
 * be testing a different object than the app runs.
 */
export function createTestFloatingWindowStore(workspaceId?: string) {
  const { projection } = projectionMemo()
  const bridge = getCRDTBridge()
  return createFloatingWindowStore({
    getWorkspaceId: () => workspaceId ?? bridge?.workspaceId() ?? null,
    projection,
  })
}
