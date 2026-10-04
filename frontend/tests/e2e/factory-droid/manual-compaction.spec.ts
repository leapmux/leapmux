import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { DROID_E2E_SKIP_REASON, droidTest } from '../droid-fixtures'
import { exerciseCompletedManualCompaction } from './compactionScenarios'

droidTest.describe('Factory Droid compaction', () => {
  droidTest.skip(!!DROID_E2E_SKIP_REASON, DROID_E2E_SKIP_REASON || '')

  droidTest('shows and keeps the native manual compaction notice', async ({ authenticatedDroidWorkspace, page, modelScript }) => {
    void authenticatedDroidWorkspace
    await exerciseCompletedManualCompaction({ page, modelScript, provider: AgentProvider.DROID })
  })
})
