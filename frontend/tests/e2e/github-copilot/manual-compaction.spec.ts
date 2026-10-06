import { copilotTest } from '../copilot-fixtures'
import { expectCompactionNoticeAfterReload } from '../helpers/compaction'
import { exerciseCopilotCompaction } from './compactionScenario'

copilotTest('manual-compaction: draws and keeps a native manual compaction notice', async ({ native }) => {
  await exerciseCopilotCompaction(native)
  await expectCompactionNoticeAfterReload(native.page)
})
