import { codewhaleTest } from '../codewhale-fixtures'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { CODEWHALE_COMPACTION } from './compactionScenario'

codewhaleTest.describe('Codewhale basic chat', () => {
  codewhaleTest('compacts a scripted conversation on request', async ({ native }) => {
    await exerciseNativeCompaction(native, CODEWHALE_COMPACTION)
  })
})
