/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { KEY_TAB_MRU, sessionStorageSet } from '~/lib/browserStorage'
import { getCRDTBridge, setCRDTBridge } from '~/lib/crdt'
import { emitSplitTile } from '~/stores/layoutOps'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { mruAgentEditorDeps } from './mruAgentEditorDeps'

beforeEach(() => sessionStorage.clear())
afterEach(() => setCRDTBridge(null))

const WS = 'ws-test'

function setup() {
  const harness = installTestBridge({ workspaceId: WS })
  const stores = createTestTabStores(WS)
  return {
    harness,
    ...stores,
    deps: mruAgentEditorDeps({
      view: stores.view,
      selection: stores.selection,
      layoutStore: stores.layoutStore,
      floatingWindowStore: stores.floatingWindowStore,
      getWorkspaceId: () => WS,
    }),
  }
}

/**
 * The wiring `insertIntoMruAgentEditor` needs, built once instead of hand-copied
 * at three call sites (TileRenderer's quote and mention handlers, and the
 * sidebar's file mention).
 */
describe('mruAgentEditorDeps', () => {
  it('offers the workspace agent tabs in MRU order', () => {
    createRoot((dispose) => {
      const s = setup()
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a' })
      emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: s.harness.rootTileId, position: 'b' })
      s.selection.setActiveById(TabType.AGENT, 'a1')
      s.selection.setActiveById(TabType.AGENT, 'a2')

      expect(s.deps.mruTabs()[0]?.id, 'most recently touched first').toBe('a2')
      dispose()
    })
  })

  it('is empty for a workspace with no tabs', () => {
    createRoot((dispose) => {
      const s = setup()
      expect(s.deps.mruTabs()).toEqual([])
      dispose()
    })
  })

  /**
   * `activate` selects AND focuses. Selecting alone is not enough:
   * `AppShell.activeTab` reads `activeTabForTile(focusedTileId())`, so an agent
   * promoted in a tile the user is not focused on would receive the inserted
   * text while the centre pane, `getCurrentTabContext` and every tab shortcut
   * kept answering for a different tab. `insertIntoMruAgentEditor` already
   * pulls DOM focus into that editor, so carrying tile focus is what makes the
   * two agree.
   */
  it('focuses the tile it activates, not just the tab', () => {
    createRoot((dispose) => {
      const s = setup()
      const split = emitSplitTile(getCRDTBridge()!, s.harness.rootTileId, 'horizontal')!
      emitAddTab({ type: TabType.AGENT, id: 'far', tileId: split.childB, position: 'a' })
      s.layoutStore.setFocusedTile(split.childA)

      s.deps.activate(s.view.getById(TabType.AGENT, 'far')!)

      expect(s.selection.activeKeyForWorkspace(WS)).toBe(tabKey({ type: TabType.AGENT, id: 'far' }))
      expect(
        s.layoutStore.focusedTileId(),
        'focus follows, or the shell keeps operating on the other tile',
      ).toBe(split.childB)
      dispose()
    })
  })

  it('leaves focus alone for a tab with no tile', () => {
    createRoot((dispose) => {
      const s = setup()
      s.layoutStore.setFocusedTile(s.harness.rootTileId)

      expect(() => s.deps.activate({
        type: TabType.AGENT,
        id: 'ghost',
        workspaceId: WS,
      } as never)).not.toThrow()
      expect(s.layoutStore.focusedTileId()).toBe(s.harness.rootTileId)
      dispose()
    })
  })

  /**
   * Regression for issue #345: before `mru` was persisted, every tab came back
   * scoring zero after a reload, so `mruOrder` silently degraded to position
   * order — and a new agent could be seeded with the wrong tab's `workingDir`.
   * The store now seeds from `KEY_TAB_MRU` on construction, so the prior
   * session's ordering survives the (simulated) reload.
   */
  it('honours the persisted MRU ordering across a reload, not position order', () => {
    // Seed the prior session's stamps: 'a1' was touched last (higher stamp),
    // even though 'a2' was added second (position order).
    sessionStorageSet(KEY_TAB_MRU, { a1: 2, a2: 1 })
    createRoot((dispose) => {
      const s = setup()
      emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: s.harness.rootTileId, position: 'a' })
      emitAddTab({ type: TabType.AGENT, id: 'a2', tileId: s.harness.rootTileId, position: 'b' })

      // 'a1' leads — the persisted stamp wins, not the add order.
      expect(s.deps.mruTabs()[0]?.id).toBe('a1')
      dispose()
    })
  })
})
