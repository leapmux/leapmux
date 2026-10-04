import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { KIMI_E2E_SKIP_REASON, kimiTest } from '../kimi-fixtures'

kimiTest.skip(!!KIMI_E2E_SKIP_REASON, KIMI_E2E_SKIP_REASON || '')

kimiTest('executes a disposable MCP echo tool', async ({ authenticatedKimiWorkspace, page, modelScript }) => {
  void authenticatedKimiWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.KIMI_CODE, 'kimi')
})
