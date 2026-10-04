import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { KIRO_E2E_SKIP_REASON, kiroTest } from '../kiro-fixtures'

kiroTest.skip(!!KIRO_E2E_SKIP_REASON, KIRO_E2E_SKIP_REASON || '')

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('compacts a scripted conversation on request', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await exerciseManualCompaction(page, modelScript, { completionText: 'Context compacted' })
  })
})
