import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('stops a native turn and accepts a new prompt after queue resume', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseInterruptTurn(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
