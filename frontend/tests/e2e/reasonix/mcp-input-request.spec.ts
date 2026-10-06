import { reasonixTest } from '../reasonix-fixtures'
import { exerciseReasonixMcpForm } from './mcpScenario'

reasonixTest('answers a native Reasonix MCP form and preserves draft values after reload', async ({ page, authenticatedEmptyWorkspace, leapmuxServer, modelScript }) => {
  await exerciseReasonixMcpForm({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId })
})
