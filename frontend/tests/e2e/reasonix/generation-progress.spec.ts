import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { applyPermissionPreset } from '../helpers/ui'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', prepare: () => applyPermissionPreset(page, 'bypass') })
})
