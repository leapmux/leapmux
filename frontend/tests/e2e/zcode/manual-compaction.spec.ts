import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_COMPACTION } from './compactionScenario'

zcodeTest('keeps the native summary and the completed notice after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, ZCODE_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
