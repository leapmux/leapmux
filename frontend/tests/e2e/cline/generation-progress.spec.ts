import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('proves the live native generation counter', async ({ page, modelScript, leapmuxServer, authenticatedClineWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedClineWorkspace.workspaceId, provider: AgentProvider.CLINE }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
