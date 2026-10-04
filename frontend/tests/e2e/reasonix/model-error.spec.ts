import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseModelError } from '../helpers/nativeModelError'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('shows the native model failure and accepts a later valid prompt', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseModelError(context)
})
