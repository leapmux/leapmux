import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeMcpInputLimit } from './mcpScenario'
import { nativeContext } from './scenarios'

zcodeTest('returns the actual native MCP form refusal without opening a form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseZCodeMcpInputLimit(await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId }))
})
