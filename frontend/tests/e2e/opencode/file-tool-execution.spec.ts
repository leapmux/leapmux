import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('reads and changes actual scratch bytes through native file tools', async ({ authenticatedOpencodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedOpencodeWorkspace.workspaceId, provider: AgentProvider.OPENCODE }
  await exerciseFileToolExecution(context)
})
