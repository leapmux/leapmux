import { AgentProvider } from '../../../src/generated/proto/leapmux/v1/agent_pb'
import { QODER_E2E_SKIP_REASON, qoderTest } from '../qoder-fixtures'
import { exerciseCompletedManualCompaction, exerciseFailedManualCompaction } from './compactionScenarios'

qoderTest.describe('Qoder CLI compaction and rate limits', () => {
  qoderTest.skip(!!QODER_E2E_SKIP_REASON, QODER_E2E_SKIP_REASON || '')

  qoderTest('uses a native manual summary and shows its completed boundary', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseCompletedManualCompaction({ page, modelScript, provider: AgentProvider.QODER })
  })

  qoderTest('does not claim a failed native manual compaction succeeded', async ({ qoderWorkspace, page, modelScript }) => {
    void qoderWorkspace
    await exerciseFailedManualCompaction({ page, modelScript, provider: AgentProvider.QODER })
  })
})
