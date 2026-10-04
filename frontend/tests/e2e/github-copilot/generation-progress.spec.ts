import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { copilotTest } from '../copilot-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { applyPermissionPreset } from '../helpers/ui'

copilotTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedCopilotWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCopilotWorkspace.workspaceId, provider: AgentProvider.GITHUB_COPILOT }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', prepare: () => applyPermissionPreset(page, 'bypass') })
})
