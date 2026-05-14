import type { installTestBridge } from '../helpers/crdtBridge'
import { create } from '@bufbuild/protobuf'
import { describe, expect, it } from 'vitest'
import {
  HLCSchema,
  LWWNodeKindSchema,
  LWWStringSchema,
  NodeKind,
  NodeRecordSchema,
  TabRecordSchema,
} from '~/generated/leapmux/v1/org_crdt_pb'
import { TabType } from '~/generated/leapmux/v1/workspace_pb'
import { tabKey } from '~/stores/tab.helpers'
import { createTabStore } from '~/stores/tab.store'
import { withTestBridge } from '../helpers/crdtBridge'

// Seed a leaf node under the seeded root so tabs can resolve to a
// workspace via parent_id chain. Returns the seeded leaf's id.
function seedLeafChild(harness: ReturnType<typeof installTestBridge>, leafId: string) {
  const hlc = create(HLCSchema, { physical: 2n, logical: 0n, clientId: 'seed' })
  harness.pending.state.confirmedState.nodes[leafId] = create(NodeRecordSchema, {
    nodeId: leafId,
    parentId: harness.rootTileId,
    kind: create(LWWNodeKindSchema, { value: NodeKind.LEAF, hlc }),
    position: create(LWWStringSchema, { value: 'N', hlc }),
  })
  harness.pending.recomputeSpeculative()
}

// Seed a TabRecord directly into the confirmed state — simulates an
// already-committed CRDT tab without going through the submit path.
function seedTab(
  harness: ReturnType<typeof installTestBridge>,
  opts: { type: TabType, id: string, tileId: string, position: string, workerId: string },
) {
  const hlc = create(HLCSchema, { physical: 3n, logical: 0n, clientId: 'seed' })
  harness.pending.state.confirmedState.tabs[opts.id] = create(TabRecordSchema, {
    tabType: opts.type,
    tabId: opts.id,
    tileId: create(LWWStringSchema, { value: opts.tileId, hlc }),
    position: create(LWWStringSchema, { value: opts.position, hlc }),
    workerId: create(LWWStringSchema, { value: opts.workerId, hlc }),
  })
  harness.pending.recomputeSpeculative()
}

describe('tab.store CRDT op emissions', () => {
  it('addTab enqueues SetTabRegister(tile_id) + position + worker_id', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: harness.rootTileId,
        position: 'N',
        workerId: 'wkr-1',
      })
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(lastBatch?.ops.length).toBe(3)
      const cases = lastBatch?.ops.map(o => o.body.case)
      expect(cases).toEqual(['setTabRegister', 'setTabRegister', 'setTabRegister'])
    })
  })

  it('addTab with silent: true skips emission', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      const before = harness.pending.state.pendingBatches.length
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: harness.rootTileId,
        position: 'N',
      }, { silent: true })
      expect(harness.pending.state.pendingBatches.length).toBe(before)
      expect(store.state.tabs.length).toBe(1)
    })
  })

  it('removeTab enqueues a single TombstoneTab op', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId })
      const before = harness.pending.state.pendingBatches.length
      store.removeTab(TabType.AGENT, 'a1')
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      expect(lastBatch?.ops.length).toBe(1)
      expect(lastBatch?.ops[0].body.case).toBe('tombstoneTab')
    })
  })

  it('removeTab with silent: true skips emission', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId })
      const before = harness.pending.state.pendingBatches.length
      store.removeTab(TabType.AGENT, 'a1', { silent: true })
      expect(harness.pending.state.pendingBatches.length).toBe(before)
      expect(store.state.tabs.length).toBe(0)
    })
  })

  it('setTabPosition emits SetTabRegister(position)', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'M' })
      const before = harness.pending.state.pendingBatches.length
      store.setTabPosition(tabKey({ type: TabType.AGENT, id: 'a1' }), 'V')
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      expect(lastBatch?.ops.length).toBe(1)
      const op = lastBatch?.ops[0]
      expect(op?.body.case).toBe('setTabRegister')
      if (op?.body.case === 'setTabRegister')
        expect(op.body.value.field.case).toBe('position')
    })
  })

  it('moveTabToTile emits SetTabRegister(tile_id) only when tile actually changes', () => {
    withTestBridge((harness) => {
      seedLeafChild(harness, 'leaf-2')
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId })
      const before = harness.pending.state.pendingBatches.length
      store.moveTabToTile(tabKey({ type: TabType.AGENT, id: 'a1' }), 'leaf-2')
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      const op = lastBatch?.ops[0]
      expect(op?.body.case).toBe('setTabRegister')
      if (op?.body.case === 'setTabRegister')
        expect(op.body.value.field.case).toBe('tileId')
    })
  })

  it('moveTabToTile is a no-op when target tile is the same as current', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId })
      const before = harness.pending.state.pendingBatches.length
      store.moveTabToTile(tabKey({ type: TabType.AGENT, id: 'a1' }), harness.rootTileId)
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    })
  })

  it('reassignTabsToTile emits one batch with one op per moved tab', () => {
    withTestBridge((harness) => {
      seedLeafChild(harness, 'leaf-x')
      seedLeafChild(harness, 'leaf-y')
      seedLeafChild(harness, 'leaf-z')
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'leaf-x' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: 'leaf-y' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'leaf-x' })
      const before = harness.pending.state.pendingBatches.length
      store.reassignTabsToTile(['leaf-x', 'leaf-y'], 'leaf-z')
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      expect(lastBatch?.ops.length).toBe(3)
      for (const op of lastBatch?.ops ?? []) {
        expect(op.body.case).toBe('setTabRegister')
        if (op.body.case === 'setTabRegister')
          expect(op.body.value.field.case).toBe('tileId')
      }
    })
  })

  it('mergeTabsIntoTile emits a batch with tile_id + position for each moved tab', () => {
    withTestBridge((harness) => {
      seedLeafChild(harness, 'leaf-src')
      seedLeafChild(harness, 'leaf-dst')
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: 'leaf-src', position: 'M' })
      store.addTab({ type: TabType.TERMINAL, id: 't1', tileId: 'leaf-src', position: 'V' })
      const before = harness.pending.state.pendingBatches.length
      store.mergeTabsIntoTile('leaf-src', 'leaf-dst')
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      // 2 tabs × (tile_id + position) = 4 ops in one batch.
      expect(lastBatch?.ops.length).toBe(4)
      const fields = (lastBatch?.ops ?? []).map((o) => {
        if (o.body.case === 'setTabRegister')
          return o.body.value.field.case
        return ''
      })
      expect(fields).toEqual(['tileId', 'position', 'tileId', 'position'])
    })
  })

  it('moveTabToWorkspace emits a single batch with tile_id + position', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      const before = harness.pending.state.pendingBatches.length
      store.moveTabToWorkspace(TabType.AGENT, 'a1', 'tile-W2', 'M')
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      expect(lastBatch?.ops.length).toBe(2)
      const fields = (lastBatch?.ops ?? []).map((o) => {
        if (o.body.case === 'setTabRegister')
          return o.body.value.field.case
        return ''
      })
      expect(fields).toEqual(['tileId', 'position'])
    })
  })

  it('reorderTabs emits SetTabRegister(position) for the moved tab', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId, position: 'A' })
      store.addTab({ type: TabType.AGENT, id: 'a2', tileId: harness.rootTileId, position: 'B' })
      store.addTab({ type: TabType.AGENT, id: 'a3', tileId: harness.rootTileId, position: 'C' })
      const before = harness.pending.state.pendingBatches.length
      const newPos = store.reorderTabs(
        tabKey({ type: TabType.AGENT, id: 'a1' }),
        tabKey({ type: TabType.AGENT, id: 'a3' }),
      )
      expect(newPos).toBeTruthy()
      const lastBatch = harness.pending.state.pendingBatches.at(-1)
      expect(harness.pending.state.pendingBatches.length).toBe(before + 1)
      expect(lastBatch?.ops.length).toBe(1)
      const op = lastBatch?.ops[0]
      expect(op?.body.case).toBe('setTabRegister')
      if (op?.body.case === 'setTabRegister')
        expect(op.body.value.field.case).toBe('position')
    })
  })
})

describe('tab.store reconcileFromProjection', () => {
  it('adds tabs that exist in the projection but not locally', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      // CRDT has a tab the local store doesn't know about (e.g. another client opened it).
      store.reconcileFromProjection({
        workspaceId: harness.workspaceId,
        renderedTabs: [{
          tabType: TabType.AGENT,
          tabId: 'a1',
          tileId: harness.rootTileId,
          position: 'M',
          workerId: 'wkr-1',
        }],
        crdtKnownTabIds: new Set(['a1']),
      })
      expect(store.state.tabs.length).toBe(1)
      const tab = store.state.tabs[0]
      expect(tab.id).toBe('a1')
      expect(tab.tileId).toBe(harness.rootTileId)
      expect(tab.position).toBe('M')
      expect(tab.workerId).toBe('wkr-1')
    })
  })

  it('removes local tabs the projection no longer renders (cross-workspace move or remote tombstone)', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.AGENT, id: 'a1', tileId: harness.rootTileId }, { silent: true })
      // Projection says the tab is no longer in this workspace, but
      // the CRDT does know about it (in another workspace, or
      // tombstoned).
      store.reconcileFromProjection({
        workspaceId: harness.workspaceId,
        renderedTabs: [],
        crdtKnownTabIds: new Set(['a1']),
      })
      expect(store.state.tabs.length).toBe(0)
    })
  })

  it('does NOT remove local tabs the CRDT has no record of (e.g. client-only file tabs before E2EE register)', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({ type: TabType.FILE, id: 'f1', tileId: harness.rootTileId }, { silent: true })
      store.reconcileFromProjection({
        workspaceId: harness.workspaceId,
        renderedTabs: [],
        // f1 is not in crdtKnownTabIds — local-only flow.
        crdtKnownTabIds: new Set(),
      })
      expect(store.state.tabs.length).toBe(1)
      expect(store.state.tabs[0].id).toBe('f1')
    })
  })

  it('updates CRDT-driven fields when they diverge', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      store.addTab({
        type: TabType.AGENT,
        id: 'a1',
        tileId: harness.rootTileId,
        position: 'M',
        workerId: 'wkr-1',
        title: 'My Agent',
      }, { silent: true })
      // CRDT now has tile-2 + position B; reconcile should sync.
      store.reconcileFromProjection({
        workspaceId: harness.workspaceId,
        renderedTabs: [{
          tabType: TabType.AGENT,
          tabId: 'a1',
          tileId: 'tile-2',
          position: 'B',
          workerId: 'wkr-1',
        }],
        crdtKnownTabIds: new Set(['a1']),
      })
      const tab = store.state.tabs[0]
      expect(tab.tileId).toBe('tile-2')
      expect(tab.position).toBe('B')
      // Worker-side metadata (title) preserved.
      expect(tab.title).toBe('My Agent')
    })
  })

  it('does not emit ops during reconcile (silent path)', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      const before = harness.pending.state.pendingBatches.length
      store.reconcileFromProjection({
        workspaceId: harness.workspaceId,
        renderedTabs: [{
          tabType: TabType.AGENT,
          tabId: 'a1',
          tileId: harness.rootTileId,
          position: 'M',
          workerId: 'wkr-1',
        }],
        crdtKnownTabIds: new Set(['a1']),
      })
      expect(harness.pending.state.pendingBatches.length).toBe(before)
    })
  })

  it('end-to-end: a remote tab arrival via the speculative state surfaces into state.tabs after a reconcile call', () => {
    withTestBridge((harness) => {
      const store = createTabStore()
      // Seed a TabRecord into the manager's confirmed state — simulates
      // the CRDT consuming a remote SetTabRegister batch.
      seedTab(harness, {
        type: TabType.TERMINAL,
        id: 'remote-term-1',
        tileId: harness.rootTileId,
        position: 'M',
        workerId: 'wkr-remote',
      })
      // AppShell would normally call this from a createEffect over
      // bridge.speculativeState; the test invokes it imperatively.
      store.reconcileFromProjection({
        workspaceId: harness.workspaceId,
        renderedTabs: [{
          tabType: TabType.TERMINAL,
          tabId: 'remote-term-1',
          tileId: harness.rootTileId,
          position: 'M',
          workerId: 'wkr-remote',
        }],
        crdtKnownTabIds: new Set(['remote-term-1']),
      })
      expect(store.state.tabs.find(t => t.id === 'remote-term-1')).toBeTruthy()
    })
  })
})
