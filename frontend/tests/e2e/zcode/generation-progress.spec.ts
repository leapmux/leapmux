import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { applyPermissionPreset } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', prepare: () => applyPermissionPreset(page, 'bypass') })
})
