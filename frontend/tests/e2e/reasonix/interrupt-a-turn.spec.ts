import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('stops a native turn and accepts a new prompt after queue resume', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseInterruptTurn(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
