import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from './codewhale-fixtures'
import { exerciseMcpEcho } from './helpers/mcpExecution'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest('executes a disposable MCP echo tool', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
  void authenticatedCodewhaleWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.CODEWHALE, 'codewhale')
})
