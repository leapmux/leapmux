import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseDeepseekHarnessCompaction } from './compactionScenarios'
import { nativeContext } from './scenarios'

deepseekHarnessTest('shows the completed native compaction boundary and keeps it after reload', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseDeepseekHarnessCompaction(context)
  await page.reload()
  await expectCompactionNotice(page)
})
