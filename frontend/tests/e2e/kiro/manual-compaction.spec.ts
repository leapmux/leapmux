import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { kiroTest } from '../kiro-fixtures'

kiroTest.describe('Kiro basic chat', () => {
  kiroTest('compacts a scripted conversation on request', async ({ authenticatedKiroWorkspace, page, modelScript }) => {
    void authenticatedKiroWorkspace
    await exerciseManualCompaction(page, modelScript, { completionText: 'Context compacted' })
  })
})
