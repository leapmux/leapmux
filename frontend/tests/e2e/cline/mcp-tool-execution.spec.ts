import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { clineTest } from '../cline-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'

clineTest('executes a disposable MCP echo tool', async ({ authenticatedClineWorkspace, page, modelScript }) => {
  void authenticatedClineWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.CLINE, 'cline')
})
