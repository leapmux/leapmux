import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CURSOR_E2E_SKIP_REASON, cursorTest } from '../cursor-fixtures'
import { exerciseCursorMcpSession } from './mcpScenario'

cursorTest.skip(!!CURSOR_E2E_SKIP_REASON, CURSOR_E2E_SKIP_REASON || '')

cursorTest('mcp-tool-execution: shows the native MCP form refusal and runs an echo tool in ACP mode', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  const context = { page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.CURSOR }
  await exerciseCursorMcpSession(context)
})
