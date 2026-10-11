import { codexTest } from '../codex-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseCodexCompaction } from './compactionScenario'

codexTest('shows a completed native compaction notice and retains it after reload', async ({ native }) => {
  await exerciseCodexCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
