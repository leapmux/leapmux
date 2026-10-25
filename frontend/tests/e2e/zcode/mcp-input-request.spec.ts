import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeMcpForm } from './mcpScenario'
import { nativeContext } from './scenarios'

zcodeTest('answers a project MCP server\'s form through the typed form, also after a reload', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseZCodeMcpForm(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }))
})
