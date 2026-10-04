import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelError } from '../helpers/nativeModelError'
import { zcodeTest } from '../zcode-fixtures'

zcodeTest('shows the native model failure and accepts a later valid prompt', async ({ authenticatedZCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedZCodeWorkspace.workspaceId, provider: AgentProvider.ZCODE }
  await exerciseModelError(context)
})
