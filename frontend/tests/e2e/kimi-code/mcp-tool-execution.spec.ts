import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { kimiTest } from '../kimi-fixtures'

kimiTest('executes a disposable MCP echo tool', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
  void authenticatedKimiWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.KIMI_CODE, 'kimi')
})
