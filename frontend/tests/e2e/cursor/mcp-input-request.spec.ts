import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorMcpSession } from './mcpScenario'

cursorTest('shows the native MCP form refusal and runs an echo tool in ACP mode', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await exerciseCursorMcpSession(context)
})
