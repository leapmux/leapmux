import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('closes the native agent and its actual owned process tree', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseCloseAgent(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
