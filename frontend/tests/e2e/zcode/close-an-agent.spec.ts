import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('closes the native agent and its actual owned process tree', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseCloseAgent(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
