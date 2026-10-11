import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { mimoTest } from '../mimo-fixtures'
import { MIMO_COMPACTION } from './compactionScenario'

mimoTest('proves native context compaction and keeps the completed compaction notice after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, MIMO_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
