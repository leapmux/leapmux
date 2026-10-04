import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest('proves the live native generation counter', async ({ page, modelScript, leapmuxServer, authenticatedKiroWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiroWorkspace.workspaceId, provider: AgentProvider.KIRO }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens' })
})
