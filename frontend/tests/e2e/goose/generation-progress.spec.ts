import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { gooseTest } from '../goose-fixtures'
import { exerciseGenerationProgress } from '../helpers/generationProgress'
import { applyPermissionPreset } from '../helpers/ui'

gooseTest('reports advancing native generation counts and keeps completed content', async ({ authenticatedGooseWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedGooseWorkspace.workspaceId, provider: AgentProvider.GOOSE }
  await exerciseGenerationProgress(context, { supported: true, counter: 'tokens', prepare: () => applyPermissionPreset(page, 'bypass') })
})
