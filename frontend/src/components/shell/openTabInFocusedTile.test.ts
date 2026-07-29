/// <reference types="vitest/globals" />
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it } from 'vitest'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { setCRDTBridge } from '~/lib/crdt'
import { tabKey } from '~/stores/tab.helpers'
import { emitAddTab } from '~/stores/tabOps'
import { installTestBridge } from '~/test-support/crdtBridge'
import { createTestTabStores } from '~/test-support/tabStores'
import { openTabInFocusedTile } from './openTabInFocusedTile'

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
   * `focusedTileId()` never answers "nothing" -- it falls back to the first leaf
   * of `projectedRoot()`, which is a LOCALLY-MINTED placeholder whenever the
   * projection has no tree for the workspace. A bootstrap that never lands
   * leaves the shell in exactly that state, and the caller has already created
   * the agent or terminal on the worker by the time it gets here: emitting
   * against the placeholder gets the batch rejected and leaves an orphan with no
   * tab to reach it by.
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
})
