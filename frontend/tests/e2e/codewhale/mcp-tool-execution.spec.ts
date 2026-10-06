import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'

codewhaleTest('executes a disposable MCP echo tool', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
  void authenticatedCodewhaleWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.CODEWHALE, 'codewhale')
})
