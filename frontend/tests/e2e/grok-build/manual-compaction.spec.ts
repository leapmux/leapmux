import { grokTest } from '../grok-fixtures'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { GROK_COMPACTION } from './compactionScenario'

grokTest.describe('Grok Build Basic Chat', () => {
  grokTest('compacts a scripted conversation on request', async ({ native }) => {
    await exerciseNativeCompaction(native, GROK_COMPACTION)
  })
})
