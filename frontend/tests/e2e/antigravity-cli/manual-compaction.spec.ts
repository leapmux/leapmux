import { expect } from '@playwright/test'
import { antigravityTest } from '../antigravity-fixtures'
import { compactionIndicator, expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'

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
