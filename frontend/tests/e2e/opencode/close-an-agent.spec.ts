import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('closes the native agent and its actual owned process tree', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseCloseAgent(context)
})
