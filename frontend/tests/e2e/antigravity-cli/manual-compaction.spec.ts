import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseFailedNativeCompaction, exerciseNativeCompaction } from '../helpers/manualCompaction'
import { messageContents, waitForAgentIdle } from '../helpers/ui'

antigravityTest('replaces old context with the native summary and keeps the compaction notice', async ({ native }) => {
  await exerciseNativeCompaction(native, {
    summary: {
      route: 'rule',
      when: { user: 'Write a continuation summary' },
    },
    summaryGate: 'antigravity-manual-summary',
    onHeld: async () => {
      await expect(compactionIndicator(native.page)).toBeVisible()
    },
  })
  await expect(compactionIndicator(native.page)).toHaveCount(0)
  await expectCompactionNoticeAfterReload(native.page, { detail: '(manual)' })
})

antigravityTest('keeps the failed compaction outcome after reload', async ({ native }) => {
  await exerciseFailedNativeCompaction(native, {
    summary: {
      route: 'rule',
      when: { user: 'Write a continuation summary' },
    },
  })
  await expect(compactionIndicator(native.page)).toHaveCount(0)
  await expect(messageContents(native.page).filter({ hasText: 'Compaction failed' })).toBeVisible()
  await native.page.reload()
  await waitForAgentIdle(native.page)
  await expect(messageContents(native.page).filter({ hasText: 'Compaction failed' })).toBeVisible()
})
