import { exerciseManualCompaction } from '../helpers/manualCompaction'
import { mimoTest } from '../mimo-fixtures'

mimoTest.describe('MiMo Code basic chat', () => {
  mimoTest('compacts a scripted conversation on request', async ({ authenticatedMiMoWorkspace, page, modelScript }) => {
    void authenticatedMiMoWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'Write a continuation summary that will allow you' })
  })
})
