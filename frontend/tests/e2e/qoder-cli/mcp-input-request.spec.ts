import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { exerciseNativeMcpForm } from './mcpScenarios'

qoderTest.describe('Qoder CLI MCP input form', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('returns zero and false values to the native MCP tool', async ({ askingQoderWorkspace, page, modelScript }) => {
    void askingQoderWorkspace
    await exerciseNativeMcpForm({ page, modelScript, provider: AgentProvider.QODER })
  })
})
