import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { reasonixTest } from '../reasonix-fixtures'

reasonixTest('clears native context without discarding saved Worker messages', async ({ authenticatedReasonixWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedReasonixWorkspace.workspaceId, provider: AgentProvider.REASONIX }
  await exerciseSessionReset(context)
})
