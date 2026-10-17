import { expect } from '@playwright/test'
import { compactionNoticeRow, expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_COMPACTION, ZCODE_MANUAL_COMPACTION_TRIGGER } from './compactionScenario'

zcodeTest('keeps the native summary and the completed notice after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, ZCODE_COMPACTION)
  // The compaction lifecycle event words the notice with its trigger and its
  // token counts. The older state rows carried no such detail.
  await expectCompactionNoticeAfterReload(native.page, { detail: ZCODE_MANUAL_COMPACTION_TRIGGER })
  await expect(compactionNoticeRow(native.page)).toContainText('→')
})
