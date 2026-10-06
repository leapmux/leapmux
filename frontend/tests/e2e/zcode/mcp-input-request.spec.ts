import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeMcpInputLimit } from './mcpScenario'

zcodeTest('returns the actual native MCP form refusal without opening a form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseZCodeMcpInputLimit({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.ZCODE })
})
