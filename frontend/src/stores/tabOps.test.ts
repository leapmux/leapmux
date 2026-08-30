/// <reference types="vitest/globals" />
import type { Tab } from './tab.types'
import type { installTestBridge } from '~/test-support/crdtBridge'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  HLCSchema,
  LWWNodeKindSchema,
  LWWStringSchema,
  NodeKind,
  NodeRecordSchema,
} from '~/generated/proto/leapmux/v1/user_crdt_pb'
import { TabType } from '~/generated/proto/leapmux/v1/workspace_pb'
import { withTestBridge } from '~/test-support/crdtBridge'
import { tabKey } from './tab.helpers'
import {
  emitAddTab,
  emitMergeTabsIntoTile,
  emitMoveTabToTile,
  emitMoveTabToWorkspace,
  emitReassignTabsToTile,
  emitRemoveTab,
  emitRemoveTabs,
  emitReorderTabs,
  emitSetTabPosition,
  hasLiveTabRecord,
  positionAfterKey,
  positionAtEnd,
} from './tabOps'

/**
 * The op-emission contract, ported from the deleted `tab.store.crdt.test.ts`.
 *
 * `tabOps` is now the ONLY way a CRDT-carried field changes — there is no
 * local write to accompany an op, and no `{ silent: true }` escape hatch (its
 * only purpose was suppressing emission for writes that came *from* the
 * projection, which no longer happens). So the exact ops each helper puts on
 * the wire are the whole of its observable behaviour, and these tests pin them.
 */

type Harness = ReturnType<typeof installTestBridge>

/** Seed a leaf under the seeded root so tabs can resolve via the parent chain. */
function seedLeafChild(harness: Harness, leafId: string) {
  const hlc = create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'seed' })
  harness.pending.state.confirmedState.nodes[leafId] = create(NodeRecordSchema, {
    nodeId: leafId,
    parentId: harness.rootTileId,
    kind: create(LWWNodeKindSchema, { value: NodeKind.LEAF, hlc }),
    position: create(LWWStringSchema, { value: 'N', hlc }),
  })
  harness.pending.recomputeSpeculative()
}

/** The ops of the most recently submitted batch. */
function lastOps(harness: Harness) {
  return harness.pending.state.pendingBatches.at(-1)?.ops ?? []
}

/** The register each `setTabRegister` op writes, in order. */
function lastFields(harness: Harness): string[] {
  return lastOps(harness).map(o => (o.body.case === 'setTabRegister' ? o.body.value.field.case ?? '' : ''))
}

/**
 * The rank the most recent batch's `setTabRegister(position)` op wrote.
 *
 * Read from the OP, not from the emitter's return value: `emitReorderTabs`
 * returns a batch id like every sibling emitter, so asserting the rank against
 * its return would have been asserting a batch id.
 */
function lastPosition(harness: Harness): string {
  for (const op of lastOps(harness)) {
    if (op.body.case === 'setTabRegister' && op.body.value.field.case === 'position')
      return op.body.value.field.value
  }
  return ''
}

function batchCount(harness: Harness): number {
  return harness.pending.state.pendingBatches.length
}

/** A `Tab`-shaped value for the helpers that take tab lists. */
function tab(id: string, tileId: string, position: string, type = TabType.AGENT): Tab {
  return { type, id, workspaceId: 'ws-test', tileId, position } as Tab
}

describe('tabOps', () => {
  describe('emitAddTab', () => {
    it('enqueues SetTabRegister for tile_id, position and worker_id', () => {
      withTestBridge((harness) => {
        emitAddTab({
          type: TabType.AGENT,
          id: 'a1',
          tileId: harness.rootTileId,
          position: 'N',
          workerId: 'wkr-1',
        })
        expect(lastOps(harness)).toHaveLength(3)
        expect(lastFields(harness)).toEqual(['tileId', 'position', 'workerId'])
      })
    })

    it('lands in speculativeState synchronously — the op IS the optimistic update', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        // No local write accompanies the emit, so if pending ops were not
        // applied on enqueue the tab would not exist anywhere until the hub
        // acked it, and every optimistic open would flicker.
        expect(harness.pending.state.speculativeState.tabs.a1).toBeDefined()
        expect(harness.pending.state.speculativeState.tabs.a1.tileId?.value).toBe(harness.rootTileId)
      })
    })

    it('omits the worker_id op when there is no worker', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        expect(lastFields(harness)).toEqual(['tileId', 'position'])
      })
    })
  })

  describe('emitRemoveTab', () => {
    it('enqueues a single TombstoneTab op', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        const before = batchCount(harness)

        emitRemoveTab(TabType.AGENT, 'a1')

        expect(batchCount(harness)).toBe(before + 1)
        expect(lastOps(harness)).toHaveLength(1)
        expect(lastOps(harness)[0].body.case).toBe('tombstoneTab')
      })
    })

    // One tombstone is the whole cleanup: it removes the tab from every
    // workspace's view at once and from peer clients, with no second
    // representation to keep in step.
    it('removes the tab from the speculative state', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        emitRemoveTab(TabType.AGENT, 'a1')
        const rec = harness.pending.state.speculativeState.tabs.a1
        expect(rec?.tombstoneAt, 'the record carries a tombstone').toBeDefined()
        expect(rec?.tombstoneAt?.physical).not.toBe(0n)
      })
    })
  })

  describe('emitRemoveTabs', () => {
    /**
     * A batch is the unit the hub charges for: it dedups the batch id,
     * validates it, commits it to the journal in its own transaction, and swaps
     * the state once. Retiring a subagent subtree one `emitRemoveTab` at a time
     * paid all of that per id.
     */
    it('tombstones every id in ONE batch', () => {
      withTestBridge((harness) => {
        for (const id of ['a1', 'a2', 'a3'])
          emitAddTab({ type: TabType.AGENT, id, tileId: harness.rootTileId, position: id })
        const before = batchCount(harness)

        emitRemoveTabs(TabType.AGENT, ['a1', 'a2', 'a3'])

        expect(batchCount(harness), 'one batch, not three').toBe(before + 1)
        expect(lastOps(harness)).toHaveLength(3)
        expect(lastOps(harness).map(op => op.body.case)).toEqual([
          'tombstoneTab',
          'tombstoneTab',
          'tombstoneTab',
        ])
        for (const id of ['a1', 'a2', 'a3'])
          expect(harness.pending.state.speculativeState.tabs[id]?.tombstoneAt).toBeDefined()
      })
    })

    // An empty batch is a round trip that says nothing.
    it('emits nothing for an empty list', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        const before = batchCount(harness)

        expect(emitRemoveTabs(TabType.AGENT, [])).toBeNull()
        expect(batchCount(harness)).toBe(before)
      })
    })
  })

  describe('hasLiveTabRecord', () => {
    /**
     * The guard on every tab id that arrives from OUTSIDE the CRDT -- today the
     * worker's descendant-agent list. The hub cannot resolve a workspace for an
     * id it has no record for, and it rejects the whole batch for it, so an
     * unfiltered list does not merely waste an op: it takes every real
     * tombstone in the batch down with it.
     */
    it('answers for a live tab, an unknown id, and a tombstoned one', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })

        expect(hasLiveTabRecord('a1'), 'a tab this account holds').toBe(true)
        expect(hasLiveTabRecord('never-opened'), 'a subagent nobody opened').toBe(false)

        emitRemoveTab(TabType.AGENT, 'a1')
        expect(hasLiveTabRecord('a1'), 'a tab already retired').toBe(false)
      })
    })
  })

  describe('emitSetTabPosition', () => {
    it('emits a single SetTabRegister(position)', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M' })
        const before = batchCount(harness)

        emitSetTabPosition(TabType.AGENT, 'a1', 'V')

        expect(batchCount(harness)).toBe(before + 1)
        expect(lastFields(harness)).toEqual(['position'])
      })
    })
  })

  describe('emitMoveTabToTile', () => {
    it('emits SetTabRegister(tile_id) when the tile changes', () => {
      withTestBridge((harness) => {
        seedLeafChild(harness, 'leaf-2')
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        const before = batchCount(harness)

        emitMoveTabToTile(TabType.AGENT, 'a1', 'leaf-2')

        expect(batchCount(harness)).toBe(before + 1)
        expect(lastFields(harness)).toEqual(['tileId'])
      })
    })

    // Drags fire a drop on the tile the tab already occupies constantly; each
    // no-op emit would be a batch the hub has to accept and every peer has to
    // apply.
    it('is a no-op when the target tile is the current one', () => {
      withTestBridge((harness) => {
        emitAddTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'N' })
        const before = batchCount(harness)

        expect(emitMoveTabToTile(TabType.AGENT, 'a1', harness.rootTileId)).toBeNull()

        expect(batchCount(harness)).toBe(before)
      })
    })
  })

  describe('emitReassignTabsToTile', () => {
    it('emits one batch with one tile_id op per moved tab', () => {
      withTestBridge((harness) => {
        seedLeafChild(harness, 'leaf-x')
        seedLeafChild(harness, 'leaf-y')
        seedLeafChild(harness, 'leaf-z')
        const tabs = [
          tab('a1', 'leaf-x', 'M'),
          tab('a2', 'leaf-y', 'M'),
          tab('t1', 'leaf-x', 'V', TabType.TERMINAL),
        ]
        const before = batchCount(harness)

        emitReassignTabsToTile(tabs, ['leaf-x', 'leaf-y'], 'leaf-z')

        expect(batchCount(harness), 'one batch, not one per tab').toBe(before + 1)
        expect(lastFields(harness)).toEqual(['tileId', 'tileId', 'tileId'])
      })
    })

    it('skips tabs that are not on one of the old tiles', () => {
      withTestBridge((harness) => {
        seedLeafChild(harness, 'leaf-x')
        seedLeafChild(harness, 'leaf-z')
        const tabs = [tab('a1', 'leaf-x', 'M'), tab('elsewhere', 'other-tile', 'M')]
        emitReassignTabsToTile(tabs, ['leaf-x'], 'leaf-z')
        expect(lastOps(harness)).toHaveLength(1)
      })
    })

    it('emits nothing when no tab matches', () => {
      withTestBridge((harness) => {
        seedLeafChild(harness, 'leaf-z')
        const before = batchCount(harness)
        expect(emitReassignTabsToTile([tab('a1', 'other', 'M')], ['leaf-x'], 'leaf-z')).toBeNull()
        expect(batchCount(harness)).toBe(before)
      })
    })
  })

  describe('emitMergeTabsIntoTile', () => {
    it('emits tile_id + position for each moved tab, in one batch', () => {
      withTestBridge((harness) => {
        seedLeafChild(harness, 'leaf-src')
        seedLeafChild(harness, 'leaf-dst')
        const sourceTabs = [tab('a1', 'leaf-src', 'M'), tab('t1', 'leaf-src', 'V', TabType.TERMINAL)]
        const before = batchCount(harness)

        emitMergeTabsIntoTile(sourceTabs, [], 'leaf-dst')

        expect(batchCount(harness)).toBe(before + 1)
        // 2 tabs x (tile_id + position) = 4 ops in one batch.
        expect(lastFields(harness)).toEqual(['tileId', 'position', 'tileId', 'position'])
      })
    })

    it('emits nothing for an empty source', () => {
      withTestBridge((harness) => {
        seedLeafChild(harness, 'leaf-dst')
        const before = batchCount(harness)
        expect(emitMergeTabsIntoTile([], [], 'leaf-dst')).toBeNull()
        expect(batchCount(harness)).toBe(before)
      })
    })
  })

  describe('emitMoveTabToWorkspace', () => {
    // Deliberately NOT tombstone-then-re-add: the hub's remove-wins rule would
    // silently drop the re-add and the tab would disappear mid-move.
    it('emits a single batch with tile_id + position', () => {
      withTestBridge((harness) => {
        const before = batchCount(harness)

        emitMoveTabToWorkspace(TabType.AGENT, 'a1', 'tile-W2', 'M')

        expect(batchCount(harness)).toBe(before + 1)
        expect(lastFields(harness)).toEqual(['tileId', 'position'])
        expect(lastOps(harness).some(o => o.body.case === 'tombstoneTab')).toBe(false)
      })
    })
  })

  describe('emitReorderTabs', () => {
    it('emits SetTabRegister(position) for the moved tab only', () => {
      withTestBridge((harness) => {
        // Lowercase seeds: LexoRank mints lowercase ranks, and comparing a
        // generated rank against an uppercase literal is always true (0x61 >
        // 0x41) -- which would make every ordering assertion below vacuous.
        const tabs = [
          tab('a1', harness.rootTileId, 'b'),
          tab('a2', harness.rootTileId, 'd'),
          tab('a3', harness.rootTileId, 'f'),
        ]
        const before = batchCount(harness)

        const newPos = emitReorderTabs(
          tabs,
          tabKey({ type: TabType.AGENT, id: 'a1' }),
          tabKey({ type: TabType.AGENT, id: 'a3' }),
        )

        expect(newPos, 'a real move returns its batch id').toBeTruthy()
        expect(batchCount(harness)).toBe(before + 1)
        expect(lastFields(harness)).toEqual(['position'])
        // The subtle half, and the reason the doc calls it out: with `moved`
        // spliced out first, a FORWARD drag inserts AFTER the target. A
        // `toBeTruthy()` check alone passes for an off-by-one that lands the
        // tab between 'd' and 'f' instead.
        const moved = lastPosition(harness)
        expect(moved > 'f', `expected a position after 'f', got ${moved}`).toBe(true)
      })
    })

    it('lands a backward drag BEFORE the target', () => {
      withTestBridge((harness) => {
        const tabs = [
          tab('a1', harness.rootTileId, 'b'),
          tab('a2', harness.rootTileId, 'd'),
          tab('a3', harness.rootTileId, 'f'),
        ]

        // Drag the last tab onto the first: it must come to rest before 'b',
        // not between 'b' and 'd'.
        const newPos = emitReorderTabs(
          tabs,
          tabKey({ type: TabType.AGENT, id: 'a3' }),
          tabKey({ type: TabType.AGENT, id: 'a1' }),
        )

        expect(newPos, 'a real move returns its batch id').toBeTruthy()
        const moved = lastPosition(harness)
        expect(moved < 'b', `expected a position before 'b', got ${moved}`).toBe(true)
      })
    })

    // The degenerate case the doc warns about: "always insert before target"
    // would make dragging onto the right neighbour a no-op.
    it('still moves when dropped on the immediate right neighbour', () => {
      withTestBridge((harness) => {
        const tabs = [
          tab('a1', harness.rootTileId, 'b'),
          tab('a2', harness.rootTileId, 'd'),
        ]
        const newPos = emitReorderTabs(
          tabs,
          tabKey({ type: TabType.AGENT, id: 'a1' }),
          tabKey({ type: TabType.AGENT, id: 'a2' }),
        )
        expect(newPos, 'the drag must produce a real move').toBeTruthy()
        const moved = lastPosition(harness)
        expect(moved > 'd', `expected a position after 'd', got ${moved}`).toBe(true)
      })
    })

    // Every sibling emitter returns the batch id so a caller can correlate
    // the submitter's later `BatchResult` with the batch it enqueued. This one
    // used to return the computed rank instead. Both are `string | null`, so
    // the correlation would type-check and then silently never match.
    it('returns the batch id, like every sibling emitter', () => {
      withTestBridge((harness) => {
        const tabs = [
          tab('a1', harness.rootTileId, 'b'),
          tab('a2', harness.rootTileId, 'd'),
        ]
        const batchId = emitReorderTabs(
          tabs,
          tabKey({ type: TabType.AGENT, id: 'a1' }),
          tabKey({ type: TabType.AGENT, id: 'a2' }),
        )
        // A LexoRank would pass `toBeTruthy()`; only a real batch id is findable
        // here, which is what a rollback handler needs to key on.
        expect(harness.pending.state.pendingBatches.at(-1)?.batchId).toBe(batchId)
      })
    })

    it('is a no-op when the tab is dropped on itself', () => {
      withTestBridge((harness) => {
        const tabs = [tab('a1', harness.rootTileId, 'A'), tab('a2', harness.rootTileId, 'B')]
        const before = batchCount(harness)
        const key = tabKey({ type: TabType.AGENT, id: 'a1' })
        expect(emitReorderTabs(tabs, key, key)).toBeNull()
        expect(batchCount(harness)).toBe(before)
      })
    })

    it('is a no-op when either key names no tab in the tile', () => {
      withTestBridge((harness) => {
        const tabs = [tab('a1', harness.rootTileId, 'A')]
        const before = batchCount(harness)
        expect(emitReorderTabs(tabs, tabKey({ type: TabType.AGENT, id: 'a1' }), 'ghost')).toBeNull()
        expect(batchCount(harness)).toBe(before)
      })
    })
  })

  // LexoRank placement, shared by every insert path. A wrong answer here shows
  // up as a tab landing in the wrong slot after a drop or a cross-workspace
  // move, with no error anywhere.
  describe('position helpers', () => {
    it('positionAtEnd sorts after every existing tab', () => {
      const tabs = [tab('a1', 't', 'A'), tab('a2', 't', 'B')]
      const pos = positionAtEnd(tabs)
      expect(pos > 'B').toBe(true)
    })

    it('positionAtEnd handles an empty tile', () => {
      expect(positionAtEnd([])).toBeTruthy()
    })

    it('positionAfterKey slots between the named tab and its successor', () => {
      const tabs = [tab('a1', 't', 'A'), tab('a2', 't', 'B'), tab('a3', 't', 'C')]
      const pos = positionAfterKey(tabs, tabKey({ type: TabType.AGENT, id: 'a1' }))
      expect(pos > 'A').toBe(true)
      expect(pos < 'B').toBe(true)
    })

    it('positionAfterKey appends when no key is given', () => {
      // "after nothing" means the end, not the front: the callers that pass
      // undefined are dropping onto empty tile space, which appends.
      const tabs = [tab('a1', 't', 'B'), tab('a2', 't', 'C')]
      expect(positionAfterKey(tabs, undefined) > 'C').toBe(true)
    })

    it('positionAfterKey falls back to the end for an unknown key', () => {
      const tabs = [tab('a1', 't', 'A'), tab('a2', 't', 'B')]
      const pos = positionAfterKey(tabs, 'ghost')
      expect(pos > 'B').toBe(true)
    })
  })
})
