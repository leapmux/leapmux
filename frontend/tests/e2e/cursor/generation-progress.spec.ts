import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'

cursorTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
