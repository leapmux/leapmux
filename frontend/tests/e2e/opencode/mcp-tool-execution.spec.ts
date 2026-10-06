import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { opencodeTest } from '../opencode-fixtures'

opencodeTest('executes a disposable MCP echo tool', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.OPENCODE, 'opencode')
})
