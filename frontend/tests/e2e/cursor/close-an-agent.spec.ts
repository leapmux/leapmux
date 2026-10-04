import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { exerciseCloseAgent } from '../helpers/nativeLifecycle'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')
cursorTest('closes the Worker agent and stops its actual native shell process tree', async ({ authenticatedCursorWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseCloseAgent({ page, modelScript, leapmuxServer, workspaceId: authenticatedCursorWorkspace.workspaceId, provider: AgentProvider.CURSOR })
})
