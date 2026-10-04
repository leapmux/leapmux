import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { kiloTest } from '../kilo-fixtures'

kiloTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
