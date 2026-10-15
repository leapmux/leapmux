import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { kiroTest } from '../kiro-fixtures'
import { KIRO_COMPACTION } from './compactionScenario'

kiroTest('retains the completed native compaction status after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, KIRO_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
