import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseNativeCompaction } from '../helpers/manualCompaction'
import { opencodeTest } from '../opencode-fixtures'
import { OPENCODE_COMPACTION } from './compactionScenario'

// OpenCode's ACP adapter forwards no compaction frame (the old limitation this
// spec tracked), and the worker's own boundary ends the compaction: the turn
// keeps one divider row, live and after a reload (decision 21, A27).
opencodeTest('ends the actual native compaction with the worker boundary row', async ({ native }) => {
  await exerciseNativeCompaction(native, OPENCODE_COMPACTION)
  await expectCompactionNoticeAfterReload(native.page, { divider: true })
})
