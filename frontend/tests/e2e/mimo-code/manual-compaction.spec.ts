import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { MIMO_E2E_SKIP_REASON, mimoTest } from '../mimo-fixtures'

mimoTest.skip(!!MIMO_E2E_SKIP_REASON, MIMO_E2E_SKIP_REASON || '')

mimoTest.describe('MiMo Code basic chat', () => {
  mimoTest('compacts a scripted conversation on request', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Write a continuation summary that will allow you' })
  })
})
