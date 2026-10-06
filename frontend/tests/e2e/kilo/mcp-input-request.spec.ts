import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseOpencodeMcpInputLimit } from '../helpers/opencodeMcpLimit'
import { kiloTest } from '../kilo-fixtures'

kiloTest('returns the actual native MCP elicitation refusal without opening a form', async ({ authenticatedEmptyWorkspace, page, modelScript, leapmuxServer }) => {
  await exerciseOpencodeMcpInputLimit({ page, modelScript, leapmuxServer, workspaceId: authenticatedEmptyWorkspace.workspaceId, provider: AgentProvider.KILO }, { binaryName: 'kilo', configurationVariable: 'KILO_CONFIG_CONTENT' })
})
