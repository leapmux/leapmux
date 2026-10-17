import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_COMPACTION, ZCODE_MANUAL_COMPACTION_TRIGGER } from './compactionScenario'

zcodeTest('compaction-notice: keeps the native summary and the completed notice after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, ZCODE_COMPACTION)
  // The compaction lifecycle event words the notice with its trigger and its
  // token counts. The stored row keeps the same words after the reload.
  await expectCompactionNoticeAfterReload(native.page, { detail: ZCODE_MANUAL_COMPACTION_TRIGGER })
})
