import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { applyPermissionPreset } from '../helpers/ui'

copilotTest('closes the native agent and its actual owned process tree', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseCloseAgent(context, { prepare: () => applyPermissionPreset(page, 'bypass') })
})
