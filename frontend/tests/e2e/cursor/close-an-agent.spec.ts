import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

cursorTest('closes the Worker agent and stops its actual native shell process tree', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseCloseAgent({ page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR })
})
