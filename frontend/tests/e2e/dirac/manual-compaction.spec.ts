import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { diracTest } from '../dirac-fixtures'
import { exerciseNativeCondense } from './compactionScenarios'

diracTest.describe('native manual compaction', () => {
  diracTest('runs the native smol command through its condense tool', async ({ authenticatedDiracWorkspace, page, modelScript }) => {
    void authenticatedDiracWorkspace
    await exerciseNativeCondense({ page, modelScript, provider: AgentProvider.DIRAC })
  })
})
