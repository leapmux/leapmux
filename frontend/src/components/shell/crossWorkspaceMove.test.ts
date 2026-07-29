import type { BatchOutcome } from '~/components/shell/useOpsSubmitter'
import { createRoot } from 'solid-js'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SIDEBAR_TAB_PREFIX } from '~/components/shell/TabDragContext'
import { useCrossWorkspaceMove } from '~/components/shell/useCrossWorkspaceMove'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { isFileTab } from '~/stores/tab.types'
import { emitAddTab } from '~/stores/tabOps'
import { flush } from '~/test-support/async'
import { installTestBridge, seedWorkspace } from '~/test-support/crdtBridge'
import { createTestFloatingWindowStore, createTestLayoutStore, createTestTabStores } from '~/test-support/tabStores'

// The cross-workspace move issues a worker RPC (then a CRDT batch) -- stub the RPCs so
// these tests assert the move as the user sees it, without a live worker. Only
// useCrossWorkspaceMove consumes these modules in this graph, so a scoped
// replacement is safe.
const mockMoveTabWorkspace = vi.fn((..._args: unknown[]) => Promise.resolve({}))
const mockRelocateFileTabPath = vi.fn((..._args: unknown[]) => Promise.resolve({}))
const mockShowWarnToast = vi.fn()

vi.mock('~/api/workerRpc', () => ({
  moveTabWorkspace: (...args: unknown[]) => mockMoveTabWorkspace(...args),
  relocateFileTabPath: (...args: unknown[]) => mockRelocateFileTabPath(...args),
}))
vi.mock('~/components/common/Toast', () => ({
  showWarnToast: (...args: unknown[]) => mockShowWarnToast(...args),
  showErrorToast: vi.fn(),
  showInfoToast: vi.fn(),
}))

/** Tab key as the join builds it: `${type}:${id}` (type is the numeric TabType enum). */
function key(type: TabType, id: string): string {
  return `${type}:${id}`
}

const ACTIVE_WS = 'ws-active'
const ACTIVE_TILE = 'tile-active'
const TARGET_WS = 'ws-target'
const TARGET_TILE = 'tile-target'
const OTHER_WS = 'ws-other'
const OTHER_TILE = 'tile-other'

/**
 * Stand up the real stores over a bridge holding THREE workspaces, plus the
 * real move handler.
 *
 * The three exist so a move can be driven between any pair without one of them
 * being "the active workspace" — the distinction the refactor removed. Every
 * workspace's tabs come from the same projection, so a tab in `ws-other` is as
 * readable as one in `ws-active`, and the assertions below read them all
 * through the same `view`.
 */
function setup(activeWsId: string = ACTIVE_WS) {
  const harness = installTestBridge({ workspaceId: ACTIVE_WS, rootTileId: ACTIVE_TILE })
  seedWorkspace(harness, TARGET_WS, TARGET_TILE)
  seedWorkspace(harness, OTHER_WS, OTHER_TILE)

  const stores = createTestTabStores(activeWsId)
  stores.layoutStore.setFocusedTile(ACTIVE_TILE)
  const floatingWindowStore = createTestFloatingWindowStore()
  const focusTile = vi.fn()
  const batchResultHandlers = new Map<string, (outcome: BatchOutcome) => void>()

  const { move } = useCrossWorkspaceMove({
    getActiveWorkspaceId: () => activeWsId,
    view: stores.view,
    selection: stores.selection,
    layoutStore: stores.layoutStore,
    floatingWindowStore,

    batchResultHandlers,
    focusTile,
  })

  let seq = 0
  return {
    ...stores,
    harness,
    floatingWindowStore,
    focusTile,
    batchResultHandlers,
    move,
    add(type: TabType, id: string, tileId: string, meta: Record<string, unknown> = {}) {
      seq += 1
      emitAddTab({ type, id, tileId, position: `p${seq}`, workerId: 'w1' })
      if (Object.keys(meta).length > 0)
        stores.metadata.patch(id, meta)
    },
  }
}

describe('useCrossWorkspaceMove', () => {
  afterEach(() => {
    vi.clearAllMocks()
  })

  it('moves a tab to the target workspace focused tile and activates it there', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))

      // Worker bookkeeping flips FIRST, before any CRDT op goes out.
      expect(mockMoveTabWorkspace).toHaveBeenCalledWith('w1', expect.objectContaining({
        tabType: TabType.AGENT,
        tabId: 'a1',
        newWorkspaceId: TARGET_WS,
      }))
      await flush()

      // One tab, one place. The source workspace no longer lists it and the
      // target does — both read off the same projection, so there is no window
      // in which the two disagree.
      expect(h.view.forWorkspace(ACTIVE_WS).map(t => t.id)).toEqual([])
      expect(h.view.forWorkspace(TARGET_WS).map(t => t.id)).toEqual(['a1'])
      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(TARGET_TILE)
      expect(h.selection.activeKeyForWorkspace(TARGET_WS)).toBe(key(TabType.AGENT, 'a1'))
      // Focus is filed under the DESTINATION workspace, not the one still on
      // screen: `setFocusedTile` keys by workspace, so passing the active one
      // would point this workspace at a tile outside its own tree and
      // `useFocusInvariant` would reset it to the first leaf.
      expect(h.focusTile).toHaveBeenCalledWith(TARGET_TILE, TARGET_WS)
      dispose()
    })
  })

  // The raw focus pointer has no liveness guarantee: only the active workspace's
  // focus is repaired by `useFocusInvariant`, so a destination the user last
  // visited before another client closed that tile keeps a dead id. Feeding it
  // to `tile_id` gets the batch rejected by the hub, which silently reverts the
  // optimistic move and drops the drag with no visible error. `focusedLeafIdFor`
  // is what makes that validation unforgettable rather than a caller's duty.
  it('ignores a remembered focus tile that is no longer a leaf in the target', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      // Pretend the user once focused a tile in TARGET_WS that has since gone.
      h.layoutStore.setFocusedTile('tile-that-was-closed', TARGET_WS)
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      // Falls through to the target's first real leaf rather than the ghost.
      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(TARGET_TILE)
      expect(h.focusTile).toHaveBeenCalledWith(TARGET_TILE, TARGET_WS)
      dispose()
    })
  })

  it('still prefers a remembered focus tile that IS a live leaf', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      // Split the target so it has a second real leaf to remember.
      const targetStore = createTestLayoutStore(TARGET_WS)
      const secondLeaf = targetStore.splitTile(TARGET_TILE, 'horizontal')!
      h.layoutStore.setFocusedTile(secondLeaf, TARGET_WS)
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(secondLeaf)
      dispose()
    })
  })

  it('carries the tab metadata across the move', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE, { title: 'Agent Olivia' })

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      // Metadata is keyed by tab id and has no workspace dimension, so a move
      // cannot lose it — there is nothing to copy between stores.
      const moved = h.view.getById(TabType.AGENT, 'a1')
      expect(moved?.title).toBe('Agent Olivia')
      expect(moved?.workerId).toBe('w1')
      expect(moved?.workspaceId).toBe(TARGET_WS)
      dispose()
    })
  })

  it('moves a FILE tab via the relocate RPC, preserving its path', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.FILE, 'f1', ACTIVE_TILE, { filePath: '/repo/a.ts' })

      h.move(TARGET_WS, key(TabType.FILE, 'f1'))

      // FILE takes RelocateFileTabPath, not MoveTabWorkspace: the path is
      // E2EE and the hub never sees it.
      expect(mockRelocateFileTabPath).toHaveBeenCalledWith('w1', expect.objectContaining({
        tabId: 'f1',
        newWorkspaceId: TARGET_WS,
      }))
      expect(mockMoveTabWorkspace).not.toHaveBeenCalled()
      await flush()

      const moved = h.view.getById(TabType.FILE, 'f1')
      expect(moved?.workspaceId).toBe(TARGET_WS)
      expect(moved && isFileTab(moved) && moved.filePath).toBe('/repo/a.ts')
      dispose()
    })
  })

  // The case the old implementation needed its whole isSourceActive /
  // isTargetActive fork for: neither end is the workspace on screen.
  it('moves a tab between two workspaces when NEITHER is the active one', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', OTHER_TILE)
      expect(h.view.getById(TabType.AGENT, 'a1')?.workspaceId).toBe(OTHER_WS)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      expect(h.view.forWorkspace(OTHER_WS).map(t => t.id)).toEqual([])
      expect(h.view.forWorkspace(TARGET_WS).map(t => t.id)).toEqual(['a1'])
      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(TARGET_TILE)
      dispose()
    })
  })

  it('moves a tab from a non-active workspace INTO the active one', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', OTHER_TILE)

      h.move('__active__', key(TabType.AGENT, 'a1'))
      await flush()

      expect(h.view.forWorkspace(ACTIVE_WS).map(t => t.id)).toEqual(['a1'])
      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(ACTIVE_TILE)
      expect(h.focusTile).toHaveBeenCalledWith(ACTIVE_TILE, ACTIVE_WS)
      dispose()
    })
  })

  it('is a no-op when source and target resolve to the same workspace', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(ACTIVE_WS, key(TabType.AGENT, 'a1'))
      await flush()

      expect(mockMoveTabWorkspace).not.toHaveBeenCalled()
      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(ACTIVE_TILE)
      dispose()
    })
  })

  it('appends to the target tile rather than displacing what is there', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'existing', TARGET_TILE)
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      // Appended last: `positionAtInsertIdx` is called with the tile's current
      // length, so the arriving tab sorts after the resident one.
      expect(h.view.forTile(TARGET_TILE).map(t => t.id)).toEqual(['existing', 'a1'])
      dispose()
    })
  })

  it('lands on an explicit target tile when one is given', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      // `h.layoutStore` projects the ACTIVE workspace, so it cannot address a
      // tile in the target. A store scoped to the target workspace can — which
      // is itself the point: the tree is in the projection, so any workspace's
      // layout is reachable without switching to it.
      const secondTile = createTestLayoutStore(TARGET_WS).splitTile(TARGET_TILE, 'horizontal')!
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'), undefined, secondTile)
      await flush()

      expect(h.view.getById(TabType.AGENT, 'a1')?.tileId).toBe(secondTile)
      dispose()
    })
  })

  // The root fallback fires whenever the destination has no remembered focus —
  // dragging onto a sidebar row the user has never visited, or one whose focus
  // was never recorded. A workspace whose root has been split has a SPLIT at
  // `rootNodeId`, and tabs may only be anchored to LEAF nodes: emitting the
  // root id there produces a batch the hub rejects, which reverts the move and
  // silently drops the drag.
  it('lands on a leaf when the target workspace root is a split', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      // Split the target's root, then forget its focus so the fallback runs.
      const targetLayout = createTestLayoutStore(TARGET_WS)
      targetLayout.splitTile(TARGET_TILE, 'horizontal')
      const leaves = targetLayout.getAllTileIds()
      expect(leaves, 'the root is now a split with two leaves').toHaveLength(2)
      expect(leaves, 'and the root id is no longer a leaf').not.toContain(TARGET_TILE)

      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)
      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      const landedOn = h.view.getById(TabType.AGENT, 'a1')?.tileId
      expect(landedOn, 'the tab is anchored to a real leaf').toBeTruthy()
      expect(leaves).toContain(landedOn)
      dispose()
    })
  })

  it('does nothing when the dragged key names no tab', async () => {
    await createRoot(async (dispose) => {
      const h = setup()

      h.move(TARGET_WS, key(TabType.AGENT, 'ghost'))
      await flush()

      expect(mockMoveTabWorkspace).not.toHaveBeenCalled()
      dispose()
    })
  })

  // The worker RPC runs BEFORE the CRDT op precisely so this ordering holds:
  // a worker that refuses the move leaves the tab where it was, with no
  // optimistic placement to unwind.
  it('leaves the tab in place and warns when the worker RPC fails', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)
      const err = new Error('worker offline')
      mockMoveTabWorkspace.mockRejectedValueOnce(err)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      expect(h.view.getById(TabType.AGENT, 'a1')?.workspaceId).toBe(ACTIVE_WS)
      expect(h.view.forWorkspace(TARGET_WS)).toEqual([])
      expect(mockShowWarnToast).toHaveBeenCalledWith('Failed to move tab', err)
      dispose()
    })
  })

  // A hub rejection (e.g. no write access to the destination) reverts the CRDT
  // half on its own; the worker's `workspace_id` has to be put back too, or the
  // two disagree about who owns the tab.
  it('reverses the worker-side move when the hub rejects the batch', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      expect(h.batchResultHandlers.size).toBe(1)
      const [batchId, handler] = [...h.batchResultHandlers.entries()][0]
      mockMoveTabWorkspace.mockClear()
      handler({ case: 'rejected' } as BatchOutcome)

      expect(mockMoveTabWorkspace).toHaveBeenCalledWith('w1', expect.objectContaining({
        tabType: TabType.AGENT,
        tabId: 'a1',
        newWorkspaceId: ACTIVE_WS,
      }))
      // The handler unregisters itself, so a later outcome for the same batch
      // can't fire a second reversal.
      expect(h.batchResultHandlers.has(batchId)).toBe(false)
      dispose()
    })
  })

  it('does not reverse the worker-side move when the batch is accepted', async () => {
    await createRoot(async (dispose) => {
      const h = setup()
      h.add(TabType.AGENT, 'a1', ACTIVE_TILE)

      h.move(TARGET_WS, key(TabType.AGENT, 'a1'))
      await flush()

      const handler = [...h.batchResultHandlers.values()][0]
      mockMoveTabWorkspace.mockClear()
      handler({ case: 'committed' } as BatchOutcome)

      expect(mockMoveTabWorkspace).not.toHaveBeenCalled()
      expect(h.view.getById(TabType.AGENT, 'a1')?.workspaceId).toBe(TARGET_WS)
      dispose()
    })
  })
})

describe('sidebar tab drag ID parsing', () => {
  it('should correctly encode sidebar tab draggable ID', () => {
    const wsId = 'ws-123'
    const tabType = TabType.AGENT
    const tabId = 'agent-456'
    const id = `${SIDEBAR_TAB_PREFIX}${wsId}:${tabType}:${tabId}`

    expect(id).toBe('sidebar-tab:ws-123:1:agent-456')
    expect(id.startsWith(SIDEBAR_TAB_PREFIX)).toBe(true)
  })

  it('should parse workspace ID and tab key from sidebar tab ID', () => {
    const id = `${SIDEBAR_TAB_PREFIX}ws-abc:${TabType.TERMINAL}:term-def`
    const rest = id.slice(SIDEBAR_TAB_PREFIX.length)
    const colonIdx = rest.indexOf(':')
    const wsId = rest.slice(0, colonIdx)
    const realTabKey = rest.slice(colonIdx + 1)

    expect(wsId).toBe('ws-abc')
    expect(realTabKey).toBe(`${TabType.TERMINAL}:term-def`)
  })

  it('should distinguish sidebar tab drags from tabbar tab drags', () => {
    const sidebarId = `${SIDEBAR_TAB_PREFIX}ws-1:1:agent-1`
    const tabbarId = '1:agent-1'

    expect(sidebarId.startsWith(SIDEBAR_TAB_PREFIX)).toBe(true)
    expect(tabbarId.startsWith(SIDEBAR_TAB_PREFIX)).toBe(false)
    expect(sidebarId.startsWith('ws-')).toBe(false) // should not be confused with workspace drag
  })
})
