import { cursorTest } from '../cursor-fixtures'
import { exerciseCursorMcpSession } from './mcpScenario'
import { nativeContext } from './scenarios'

cursorTest('mcp-tool-execution: shows the native MCP form refusal and runs an echo tool in ACP mode', async ({ authenticatedEmptyWorkspace, leapmuxServer, page, modelScript }) => {
  await exerciseCursorMcpSession(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }))
})
