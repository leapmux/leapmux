import { antigravityTest } from '../antigravity-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'

antigravityTest('shows the completed native compaction notice and retains it after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, {
    summary: {
      route: 'rule',
      when: { user: 'Write a continuation summary' },
    },
  })
  await expectCompactionNoticeAfterReload(native.page)
})
