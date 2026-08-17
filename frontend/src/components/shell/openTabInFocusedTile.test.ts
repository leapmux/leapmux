/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { hasPlaceableTab, openTabInFocusedTile } from './openTabInFocusedTile'

afterEach(() => setCRDTBridge(null))

const WS = 'ws-test'

function setup() {
  const harness = installTestBridge({ workspaceId: WS })
  return { harness, ...createTestTabStores(WS) }
}

/** Stores pointed at a workspace the bootstrap never delivered. */
function setupUnbootstrapped() {
  const harness = installTestBridge({ workspaceId: WS })
  return { harness, ...createTestTabStores('ws-never-bootstrapped') }
}

/**
 * The five "open a tab" paths all did the same five steps in the same order,
 * and the ORDER is the part worth pinning: metadata is written BEFORE the
 * placement op, because `emitAddTab` applies to `speculativeState`
 * synchronously — the projection renders the tab before the call returns, so
 * patching afterwards paints an untitled tab for a frame.
 */
describe('openTabInFocusedTile', () => {
  it('writes metadata before the tab is projected', () => {
    createRoot((dispose) => {
      const s = setup()
      s.layoutStore.setFocusedTile(s.harness.rootTileId)

      openTabInFocusedTile(s, { type: TabType.AGENT, id: 'a1', workerId: 'w1' }, { title: 'My Agent' })

      // The tab exists in the join AND already carries its title: if the patch
      // ran after the op, the first render would show a bare row.
      const tab = s.view.getById(TabType.AGENT, 'a1')
      expect(tab?.title).toBe('My Agent')
      expect(tab?.tileId).toBe(s.harness.rootTileId)
      expect(tab?.workerId).toBe('w1')
      dispose()
    })
  })

  it('selects the new tab and returns the tile it landed on', () => {
    createRoot((dispose) => {
      const s = setup()
      s.layoutStore.setFocusedTile(s.harness.rootTileId)

      const tileId = openTabInFocusedTile(s, { type: TabType.AGENT, id: 'a1' }, {})

      expect(tileId).toBe(s.harness.rootTileId)
      expect(s.selection.activeKeyForTile(tileId)).toBe(tabKey({ type: TabType.AGENT, id: 'a1' }))
      expect(s.selection.activeKeyForWorkspace(WS)).toBe(tabKey({ type: TabType.AGENT, id: 'a1' }))
      dispose()
    })
  })

  it('places the new tab immediately after the tile current selection', () => {
    createRoot((dispose) => {
      const s = setup()
      const tile = s.harness.rootTileId
      s.layoutStore.setFocusedTile(tile)
      emitAddTab({ type: TabType.AGENT, id: 'first', tileId: tile, position: 'a' })
      emitAddTab({ type: TabType.AGENT, id: 'last', tileId: tile, position: 'z' })
      // The user is on `first`, so the new tab belongs next to it -- not at the
      // end of the strip.
      s.selection.setActiveById(TabType.AGENT, 'first')

      openTabInFocusedTile(s, { type: TabType.AGENT, id: 'fresh' }, {})

      expect(s.view.forTile(tile).map(t => t.id)).toEqual(['first', 'fresh', 'last'])
      dispose()
    })
  })

  it('appends when the tile has no selection to anchor to', () => {
    createRoot((dispose) => {
      const s = setup()
      const tile = s.harness.rootTileId
      s.layoutStore.setFocusedTile(tile)
      emitAddTab({ type: TabType.AGENT, id: 'first', tileId: tile, position: 'a' })

      openTabInFocusedTile(s, { type: TabType.AGENT, id: 'fresh' }, {})

      expect(s.view.forTile(tile).map(t => t.id)).toEqual(['first', 'fresh'])
      dispose()
    })
  })

  /**
   * `placementTileId()` answers `''` while the projection has no tree for
   * the workspace — the locally-minted placeholder leaf is not a placeable
   * tile. A bootstrap that never lands leaves the shell in exactly that
   * state, and the caller has already created the agent or terminal on the
   * worker by the time it gets here: emitting against the placeholder gets
   * the batch rejected and leaves an orphan with no tab to reach it by.
   */
  it('refuses to place a tab when the workspace has no projected tree', () => {
    createRoot((dispose) => {
      const s = setupUnbootstrapped()

      const tileId = openTabInFocusedTile(s, { type: TabType.AGENT, id: 'orphan' }, {})

      expect(tileId, 'no real tile to place it on').toBe('')
      expect(s.view.all(), 'and nothing was emitted against the placeholder').toEqual([])
      dispose()
    })
  })

  /**
   * Focus may legally sit on a floating-window tile, which the main tree
   * does not contain — an op naming it is rejected by the hub. Placement
   * falls back to the first main leaf, the same fallback `focusedTileId`
   * applies when nothing is focused, so a popped-out window never blocks
   * tab creation.
   */
  it('falls back to the first main leaf when focus sits on a floating tile', () => {
    createRoot((dispose) => {
      const s = setup()
      s.layoutStore.setFocusedTile('floating-tile-1')

      const tileId = openTabInFocusedTile(s, { type: TabType.AGENT, id: 'a1' }, {})

      expect(tileId).toBe(s.harness.rootTileId)
      expect(s.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(s.harness.rootTileId)
      dispose()
    })
  })

  /** The pre-RPC form of the refusals above, consulted by every tab-creation path. */
  it('hasPlaceableTab answers whether placement is possible right now', () => {
    createRoot((dispose) => {
      const ready = setup()
      ready.layoutStore.setFocusedTile(ready.harness.rootTileId)
      expect(hasPlaceableTab(ready.layoutStore)).toBe(true)

      const floatingFocused = setup()
      floatingFocused.layoutStore.setFocusedTile('floating-tile-1')
      expect(hasPlaceableTab(floatingFocused.layoutStore), 'a floating focus still places on the main tree').toBe(true)

      const unbootstrapped = setupUnbootstrapped()
      expect(hasPlaceableTab(unbootstrapped.layoutStore), 'placeholder leaf is not a placeable tile').toBe(false)
      dispose()
    })
  })
})
