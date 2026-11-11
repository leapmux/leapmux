import { describe, expect, it } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { setTabTileId } from '~/lib/crdt'
import { flush } from '~/test-support/async'
import { withTestBridge } from '~/test-support/crdtBridge'
import { createCanonicalTabPublisher } from '~/test-support/tabPublication'
import { createTestTabStores } from '~/test-support/tabStores'

describe('createTabView', () => {
  it('reads canonical initial file preferences without local presentation metadata', () => withTestBridge(async (harness) => {
    const stores = createTestTabStores(harness.workspaceId)
    const hub = createCanonicalTabPublisher(harness)
    hub.publish(TabType.FILE, 'file')
    hub.presentation(TabType.FILE, 'file', { case: 'displayMode', value: 2 })
    hub.presentation(TabType.FILE, 'file', { case: 'fileViewMode', value: 4 })
    hub.presentation(TabType.FILE, 'file', { case: 'fileDiffBase', value: 'head-vs-staged' })
    await flush()
    expect(stores.view.getById(TabType.FILE, 'file')).toMatchObject({
      displayMode: 'source',
      fileViewMode: 'unified-diff',
      fileDiffBase: 'head-vs-staged',
    })
    expect(stores.metadata.get('file')?.displayMode).toBeUndefined()
    expect(stores.metadata.get('file')?.fileViewMode).toBeUndefined()
    expect(stores.metadata.get('file')?.fileDiffBase).toBeUndefined()
    expect(harness.pending.state.pendingBatches).toHaveLength(0)
  }))

  it('invalidates the projection cache for each presentation register independently', () => withTestBridge(async (harness) => {
    const stores = createTestTabStores(harness.workspaceId)
    const hub = createCanonicalTabPublisher(harness)
    hub.publish(TabType.FILE, 'file')
    const original = stores.projection()?.renderedTabs[0]
    hub.presentation(TabType.FILE, 'file', { case: 'displayMode', value: 3 })
    expect(stores.projection()?.renderedTabs[0]).not.toBe(original)
    expect(stores.view.getById(TabType.FILE, 'file')).toMatchObject({ displayMode: 'split' })
    const display = stores.projection()?.renderedTabs[0]
    hub.presentation(TabType.FILE, 'file', { case: 'fileViewMode', value: 2 })
    expect(stores.projection()?.renderedTabs[0]).not.toBe(display)
    expect(stores.view.getById(TabType.FILE, 'file')).toMatchObject({ fileViewMode: 'head' })
    const view = stores.projection()?.renderedTabs[0]
    hub.presentation(TabType.FILE, 'file', { case: 'fileDiffBase', value: 'head-vs-working' })
    expect(stores.projection()?.renderedTabs[0]).not.toBe(view)
    expect(stores.view.getById(TabType.FILE, 'file')).toMatchObject({ fileDiffBase: 'head-vs-working' })
    await flush()
  }))

  it('keeps later per-client overrides when canonical preferences change', () => withTestBridge(async (harness) => {
    const stores = createTestTabStores(harness.workspaceId)
    const hub = createCanonicalTabPublisher(harness)
    hub.publish(TabType.FILE, 'file')
    stores.metadata.patch('file', { displayMode: 'source', fileViewMode: 'split-diff', fileDiffBase: 'head-vs-staged' })
    hub.presentation(TabType.FILE, 'file', { case: 'displayMode', value: 1 })
    hub.presentation(TabType.FILE, 'file', { case: 'fileViewMode', value: 1 })
    hub.presentation(TabType.FILE, 'file', { case: 'fileDiffBase', value: 'head-vs-working' })
    await flush()
    expect(stores.view.getById(TabType.FILE, 'file')).toMatchObject({
      displayMode: 'source',
      fileViewMode: 'split-diff',
      fileDiffBase: 'head-vs-staged',
    })
    expect(harness.pending.state.pendingBatches).toHaveLength(0)
  }))

  it('refreshes canonical preferences while holding an unresolved placement', () => withTestBridge(async (harness) => {
    const stores = createTestTabStores(harness.workspaceId)
    const hub = createCanonicalTabPublisher(harness)
    hub.publish(TabType.FILE, 'file')
    expect(stores.view.getById(TabType.FILE, 'file')?.tileId).toBe(harness.rootTileId)
    hub.deliver([
      setTabTileId(hub.context, TabType.FILE, 'file', 'not-yet-delivered'),
    ])
    hub.presentation(TabType.FILE, 'file', { case: 'displayMode', value: 3 })
    hub.presentation(TabType.FILE, 'file', { case: 'fileViewMode', value: 5 })
    hub.presentation(TabType.FILE, 'file', { case: 'fileDiffBase', value: 'head-vs-staged' })
    await flush()
    expect(stores.view.getById(TabType.FILE, 'file')).toMatchObject({
      tileId: harness.rootTileId,
      displayMode: 'split',
      fileViewMode: 'split-diff',
      fileDiffBase: 'head-vs-staged',
    })
  }))

  it.each([TabType.AGENT, TabType.TERMINAL, TabType.IMAGE])('ignores file preferences on tab type %s', type => withTestBridge(async (harness) => {
    const stores = createTestTabStores(harness.workspaceId)
    const hub = createCanonicalTabPublisher(harness)
    hub.publish(type, 'other')
    hub.presentation(type, 'other', { case: 'displayMode', value: 3 })
    hub.presentation(type, 'other', { case: 'fileViewMode', value: 5 })
    hub.presentation(type, 'other', { case: 'fileDiffBase', value: 'head-vs-staged' })
    await flush()
    const tab = stores.view.getById(type, 'other')
    expect(tab).toBeDefined()
    expect(tab).not.toHaveProperty('displayMode')
    expect(tab).not.toHaveProperty('fileViewMode')
    expect(tab).not.toHaveProperty('fileDiffBase')
  }))
})
