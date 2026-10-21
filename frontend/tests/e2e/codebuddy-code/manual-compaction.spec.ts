import { codebuddyTest } from '../codebuddy-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseContextCompaction } from './compactionScenarios'

// The compacted context replaces the old context, the summary itself draws no
// bubble, and the worker's boundary row is the one record the compaction
// leaves (decision 21).
codebuddyTest.describe('CodeBuddy Code compaction notice', () => {
  codebuddyTest('replaces old context after a native manual compaction and keeps the boundary row', async ({ native }) => {
    await exerciseContextCompaction(native)
    await expectCompactionNoticeAfterReload(native.page, { divider: true })
  })
})
