import { AgentProvider } from '../../src/generated/proto/leapmux/v1/agent_pb'
import { exerciseMcpEcho } from './helpers/mcpExecution'
import { KILO_E2E_SKIP_REASON, kiloTest } from './kilo-fixtures'

kiloTest.skip(!!KILO_E2E_SKIP_REASON, KILO_E2E_SKIP_REASON || '')

kiloTest('executes a disposable MCP echo tool', async ({ authenticatedKiloWorkspace, page, modelScript }) => {
  void authenticatedKiloWorkspace
  await exerciseMcpEcho(page, modelScript, AgentProvider.KILO, 'kilo')
})
