import { junieTest } from '../junie-fixtures'
import { exerciseCompressAcknowledgement } from './compactionScenarios'

junieTest.describe('native manual compaction', () => {
  junieTest('keeps prior context after the native compress acknowledgement', async ({ native }) => {
    await exerciseCompressAcknowledgement(native)
  })
})
