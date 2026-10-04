import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { kiloTest } from '../kilo-fixtures'

kiloTest('reads and changes actual scratch bytes through native file tools', async ({ authenticatedKiloWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedKiloWorkspace.workspaceId, provider: AgentProvider.KILO }
  await exerciseFileToolExecution(context)
})
