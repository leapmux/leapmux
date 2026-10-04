import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
