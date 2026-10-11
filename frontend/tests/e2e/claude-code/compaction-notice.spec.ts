import { claudeTest } from '../claude-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { CLAUDE_COMPACTION } from './compactionScenario'

claudeTest('shows a completed native compaction notice and retains it after reload', async ({ native }) => {
  await exerciseNativeCompaction(native, CLAUDE_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page)
})
