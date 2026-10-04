import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from '../helpers/mcpExecution'
import { OPENCODE_E2E_SKIP_REASON, opencodeTest } from '../opencode-fixtures'

opencodeTest.skip(!!OPENCODE_E2E_SKIP_REASON, OPENCODE_E2E_SKIP_REASON || '')

opencodeTest('executes a disposable MCP echo tool', async ({ authenticatedOpencodeWorkspace, page, modelScript }) => {
  void authenticatedOpencodeWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.OPENCODE, 'opencode')
})
