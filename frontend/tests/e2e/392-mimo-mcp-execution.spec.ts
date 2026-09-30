import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from './helpers/mcpExecution'
import { MIMO_E2E_SKIP_REASON, mimoTest } from './mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest('executes a disposable MCP echo tool', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
  void authenticatedMiMoWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.MIMO_CODE, 'mimo')
})
