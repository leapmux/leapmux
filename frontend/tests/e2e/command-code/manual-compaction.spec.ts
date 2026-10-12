import { commandCodeTest } from '../command-code-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseCommandCodeCompaction } from './compactionScenarios'

commandCodeTest('runs the actual native summarizer and retains the completed compaction notice', async ({ native }) => {
  await exerciseCommandCodeCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
