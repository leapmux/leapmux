import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseDeepseekHarnessCompaction } from './compactionScenarios'

deepseekHarnessTest('shows the completed native compaction boundary and keeps it after reload', async ({ native }) => {
  await exerciseDeepseekHarnessCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
