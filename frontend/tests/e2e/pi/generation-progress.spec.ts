import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { piTest } from '../pi-fixtures'

piTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
