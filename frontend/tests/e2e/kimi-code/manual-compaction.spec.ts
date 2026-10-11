import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { kimiTest } from '../kimi-fixtures'
import { KIMI_COMPACTION } from './compactionScenario'

kimiTest.describe('Kimi Code compaction notice', () => {
  kimiTest('keeps the native summary and removes old context after manual compaction', async ({ native }) => {
    await exerciseNativeCompaction(native, KIMI_COMPACTION)
    await expectCompactionNotice(native.page)
  })
})
