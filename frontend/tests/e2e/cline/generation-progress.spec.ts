import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'

clineTest('proves the live native generation counter', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
