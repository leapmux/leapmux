import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { CLINE_E2E_SKIP_REASON, clineTest } from '../cline-fixtures'
import { exerciseMcpEcho } from '../helpers/mcpExecution'

clineTest.skip(!!CLINE_E2E_SKIP_REASON, CLINE_E2E_SKIP_REASON || '')

clineTest('executes a disposable MCP echo tool', async ({ authenticatedClineWorkspace, page, modelScript }) => {
  void authenticatedClineWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.CLINE, 'cline')
})
