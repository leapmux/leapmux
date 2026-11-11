import { create } from '@bufbuild/protobuf'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import { TabCreationSchema, TabDisplayMode, TabFileViewMode } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import { withTestBridge } from './crdtBridge'
import { createCanonicalTabPublisher } from './tabPublication'

afterEach(() => vi.restoreAllMocks())

describe('createCanonicalTabPublisher', () => {
  it('supplies a rank from the canonical lowercase alphabet', () => {
    withTestBridge((harness) => {
      const publisher = createCanonicalTabPublisher(harness)
      const publication = publisher.publish(TabType.FILE, 'published-file')
      expect(publication.position).toMatch(/^[a-z]+$/)
      expect(harness.pending.state.speculativeState.tabs['published-file']?.position?.value).toBe(publication.position)
    })
  })

  it('delivers placement and initial presentation as one canonical batch', () => {
    withTestBridge((harness) => {
      const publisher = createCanonicalTabPublisher(harness)
      const consume = vi.spyOn(harness.pending, 'consumeRemote')
      publisher.publishCreation(TabType.FILE, 'worker', create(TabCreationSchema, {
        tabId: 'published-file',
        placement: {
          workspaceId: harness.workspaceId,
          destination: { case: 'tileId', value: harness.rootTileId },
        },
        presentation: { displayMode: TabDisplayMode.RENDER, fileViewMode: TabFileViewMode.UNIFIED_DIFF, fileDiffBase: 'head-vs-working' },
      }))
      expect(consume).toHaveBeenCalledOnce()
      const tab = harness.pending.state.speculativeState.tabs['published-file']!
      expect(tab.tileId?.value).toBe(harness.rootTileId)
      expect(tab.displayMode?.value).toBe(1)
      expect(tab.fileViewMode?.value).toBe(4)
      expect(tab.fileDiffBase?.value).toBe('head-vs-working')
    })
  })

  it('preserves explicitly supplied zero and empty presentation registers', () => {
    withTestBridge((harness) => {
      const publisher = createCanonicalTabPublisher(harness)
      publisher.publishCreation(TabType.FILE, 'worker', create(TabCreationSchema, {
        tabId: 'published-file',
        placement: {
          workspaceId: harness.workspaceId,
          destination: { case: 'tileId', value: harness.rootTileId },
        },
        presentation: { displayMode: 0, fileViewMode: 0, fileDiffBase: '' },
      }))
      const tab = harness.pending.state.speculativeState.tabs['published-file']!
      expect(tab.displayMode?.value).toBe(0)
      expect(tab.fileViewMode?.value).toBe(0)
      expect(tab.fileDiffBase?.value).toBe('')
    })
  })
})
