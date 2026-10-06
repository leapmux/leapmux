import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { mimoTest } from '../mimo-fixtures'

mimoTest('executes a disposable MCP echo tool', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
  void authenticatedMiMoWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.MIMO_CODE, 'mimo')
})
