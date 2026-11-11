import type { TestBridgeHandle } from './crdtBridge'
import type { TestTabStores } from './tabStores'
import type { TabType } from '~/generated/proto/leapmux/v1/common_pb'
import type { TabCreation, TabPresentation } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import type { CrdtOp, SetTabRegisterOp } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { create } from '@bufbuild/protobuf'
import { useTabCreation } from '~/components/shell/useTabCreation'
import { PublishedTabSchema } from '~/generated/proto/leapmux/v1/tab_creation_pb'
import { CrdtOpSchema } from '~/generated/proto/leapmux/v1/user_ops_pb'
import { getCRDTBridge, HLCClock, newBatch, setTabPosition, setTabTileId, setTabWorkerId } from '~/lib/crdt'
import { createRepoGitStore } from '~/stores/repoGit.store'

/** Deliver committed Hub writes through the production pending manager. */
export function createCanonicalTabPublisher(harness: TestBridgeHandle) {
  const context = {
    originClientId: 'hub',
    clock: new HLCClock('hub'),
  }
  let nextOperation = 0

  function deliver(ops: CrdtOp[]) {
    for (const op of ops)
      op.canonicalHlc = context.clock.tick()
    harness.pending.consumeRemote(newBatch(ops))
  }

  function receipt(type: TabType, id: string, at: { workspaceId?: string, tileId?: string, position?: string } = {}) {
    return create(PublishedTabSchema, {
      tab: { tabType: type, tabId: id },
      workspaceId: at.workspaceId ?? harness.workspaceId,
      tileId: at.tileId ?? harness.rootTileId,
      position: at.position ?? 'n',
    })
  }

  function publish(type: TabType, id: string, at: { workspaceId?: string, tileId?: string, workerId?: string, position?: string, presentation?: TabPresentation | undefined } = {}) {
    const result = receipt(type, id, at)
    const ops = [
      setTabTileId(context, type, id, result.tileId),
      setTabPosition(context, type, id, result.position),
      setTabWorkerId(context, type, id, at.workerId ?? 'w1'),
    ]
    const preferences = at.presentation
    if (preferences?.displayMode !== undefined)
      ops.push(presentationOp(type, id, { case: 'displayMode', value: preferences.displayMode }))
    if (preferences?.fileViewMode !== undefined)
      ops.push(presentationOp(type, id, { case: 'fileViewMode', value: preferences.fileViewMode }))
    if (preferences?.fileDiffBase !== undefined)
      ops.push(presentationOp(type, id, { case: 'fileDiffBase', value: preferences.fileDiffBase }))
    deliver(ops)
    return result
  }

  function presentationOp(type: TabType, id: string, field: SetTabRegisterOp['field']): CrdtOp {
    nextOperation++
    return create(CrdtOpSchema, {
      opId: `presentation-${nextOperation}`,
      originClientId: context.originClientId,
      body: {
        case: 'setTabRegister',
        value: {
          tab: { tabType: type, tabId: id },
          field,
        },
      },
    })
  }

  function presentation(type: TabType, id: string, field: SetTabRegisterOp['field']) {
    deliver([
      presentationOp(type, id, field),
    ])
  }

  function publishCreation(type: TabType, workerId: string, creation: TabCreation | undefined) {
    if (!creation?.placement)
      throw new Error('The test Worker requires a creation intent.')
    const placement = creation.placement
    const tileId = placement.destination.case === 'tileId'
      ? placement.destination.value
      : harness.pending.state.speculativeState.workspaces[placement.workspaceId]?.rootNodeId
    if (!tileId)
      throw new Error('The test Worker requires a canonical workspace root.')
    return publish(type, creation.tabId, {
      workerId,
      tileId,
      workspaceId: placement.workspaceId,
      presentation: creation.presentation,
    })
  }

  return {
    context,
    publishCreation,
    deliver,
    receipt,
    publish,
    presentation,
  }
}

/** Wire the production creation coordinator to an installed reactive test bridge. */
export function createTestTabCreation(stores: Pick<TestTabStores, 'view' | 'metadata' | 'selection' | 'layoutStore'>, repoGitStore: ReturnType<typeof createRepoGitStore> = createRepoGitStore()) {
  const bridge = getCRDTBridge()
  if (!bridge)
    throw new Error('Install the test CRDT bridge before creating the coordinator.')
  return useTabCreation({
    ...stores,
    repoGitStore,
    userId: () => bridge.speculativeState()?.userId,
    state: bridge.speculativeState,
    activeWorkspaceId: bridge.workspaceId,
    settingsPendingAxes: () => new Set(),
    selectWorkspace: () => {},
    focusEditor: () => {},
  })
}
