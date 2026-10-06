import { droidTest } from '../droid-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseCompletedManualCompaction } from './compactionScenarios'

droidTest('keeps the completed native compaction status after reload', async ({ native }) => {
  await exerciseCompletedManualCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
