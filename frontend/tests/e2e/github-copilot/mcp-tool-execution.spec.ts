import { copilotTest } from '../copilot-fixtures'
import { exerciseCopilotMcpForm } from './mcpScenario'

copilotTest('mcp-tool-execution: submits zero and false through a native MCP form after reload', async ({ authenticatedEmptyWorkspace, leapmuxServer, modelScript, page }) => {
  await exerciseCopilotMcpForm({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
})
