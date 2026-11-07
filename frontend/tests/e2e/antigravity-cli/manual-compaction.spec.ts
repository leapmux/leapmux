import { antigravityTest } from '../antigravity-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'

antigravityTest('replaces old context with the native summary and keeps the compaction notice', async ({ native }) => {
  await exerciseNativeCompaction(native, {
    summary: {
      route: 'rule',
      when: { user: 'Write a continuation summary' },
    },
  })
  await expectCompactionNoticeAfterReload(native.page)
})
