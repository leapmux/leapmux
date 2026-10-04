import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseInterruptTurn } from '../helpers/nativeLifecycle'
import { piTest } from '../pi-fixtures'

piTest('stops a native turn and accepts a new prompt after queue resume', async ({ authenticatedPiWorkspace, page, modelScript, leapmuxServer }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedPiWorkspace.workspaceId, provider: AgentProvider.PI }
  await exerciseInterruptTurn(context)
})
