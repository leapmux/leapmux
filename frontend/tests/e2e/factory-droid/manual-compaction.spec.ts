import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { droidTest } from '../droid-fixtures'
import { exerciseCompletedManualCompaction } from './compactionScenarios'

droidTest.describe('Factory Droid compaction', () => {
  droidTest('shows and keeps the native manual compaction notice', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await exerciseCompletedManualCompaction({ page, modelScript, provider: AgentProvider.DROID })
  })
})
