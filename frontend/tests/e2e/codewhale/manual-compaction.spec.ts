import { CODEWHALE_E2E_SKIP_REASON, codewhaleTest } from '../codewhale-fixtures'
import { exerciseManualCompaction } from '../helpers/manualCompaction'

codewhaleTest.skip(!!CODEWHALE_E2E_SKIP_REASON, CODEWHALE_E2E_SKIP_REASON || '')

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('compacts a scripted conversation on request', async ({ authenticatedCodewhaleWorkspace, page, modelScript }) => {
    void authenticatedCodewhaleWorkspace
    await exerciseManualCompaction(page, modelScript, { summaryRequestMarker: 'You are performing a context checkpoint compaction' })
  })
})
