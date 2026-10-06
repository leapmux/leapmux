import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseManualCompaction } from '../helpers/manualCompaction'

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('compacts a scripted conversation on request', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'You are performing a context checkpoint compaction' })
  })
})
