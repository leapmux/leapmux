import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DIRAC_E2E_SKIP_REASON, diracTest } from '../dirac-fixtures'
import { exerciseNativeCondense } from './compactionScenarios'

diracTest.describe('native manual compaction', () => {
  diracTest.skip(!!DIRAC_E2E_SKIP_REASON, DIRAC_E2E_SKIP_REASON || '')

  diracTest('runs the native smol command through its condense tool', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await exerciseNativeCondense({ page, modelScript, provider: AgentProvider.DIRAC })
  })
})
