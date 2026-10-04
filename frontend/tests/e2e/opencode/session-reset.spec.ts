import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseSessionReset } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('clears native context without discarding saved Worker messages', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseSessionReset(context)
})
