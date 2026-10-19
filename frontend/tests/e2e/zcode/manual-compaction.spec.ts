import { expect } from '@playwright/test'
import { withCleanup } from '../helpers/cleanup'
import { compactionIndicator, compactionNoticeRow, compactionNoticeRows, expectCompactionNoticeAfterReload, expectNoLiveCompaction } from '../helpers/compaction'
import { exerciseNativeCompaction, MANUAL_COMPACTION_SUMMARY, seedManualCompactionConversation } from '../helpers/manualCompaction'
import { sendMessage, waitForAgentIdle } from '../helpers/ui'
import { zcodeTest } from '../zcode-fixtures'
import { ZCODE_COMPACTION, ZCODE_MANUAL_COMPACTION_TRIGGER } from './compactionScenario'

zcodeTest('keeps the native summary and the completed notice after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, ZCODE_COMPACTION)
  // The compaction lifecycle event words the notice with its trigger and its
  // token counts. The older state rows carried no such detail.
  await expectCompactionNoticeAfterReload(native.page, { detail: ZCODE_MANUAL_COMPACTION_TRIGGER })
  await expect(compactionNoticeRow(native.page)).toContainText('→')
})

zcodeTest('a manual compaction shows a live row while it runs and stores only its end', async ({ native }) => {
  const { page, modelScript } = native
  await seedManualCompactionConversation(native)
  const gate = 'zcode-compaction-hold'
  const summaryStep = await modelScript.queue({ text: MANUAL_COMPACTION_SUMMARY, gate })
  await withCleanup(async () => {
    await sendMessage(page, '/compact')
    await modelScript.waitForGate(gate)
    await expect(compactionIndicator(page)).toBeVisible()
    await expect(compactionNoticeRows(page)).toHaveCount(0)
    await modelScript.releaseGate(gate)
    await modelScript.waitForSteps(summaryStep + 1)
    await waitForAgentIdle(page)
    await expectNoLiveCompaction(page)
    await expectCompactionNoticeAfterReload(page, { count: 1, divider: true })
  }, async () => { await modelScript.releaseGateIfHeld(gate) })
})
