import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { ampTest } from '../amp-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'

ampTest('proves the native stream supplies no live counter', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  await exerciseGenerationProgress(context, { supported: false, counter: 'tokens' })
})

ampTest('proves real native shell output supplies no live byte counter', async ({ page, modelScript, leapmuxServer, authenticatedAmpWorkspace }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedAmpWorkspace.workspaceId, provider: AgentProvider.AMP }
  await exerciseGenerationProgress(context, { supported: false, counter: 'bytes' })
})
