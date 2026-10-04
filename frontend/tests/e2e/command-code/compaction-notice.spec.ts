import { commandCodeTest } from '../command-code-fixtures'
import { expectCompactionNotice } from '../helpers/compaction'
import { exerciseCommandCodeCompaction } from './compactionScenarios'
import { nativeContext } from './scenarios'

commandCodeTest('runs the actual native summarizer and retains the completed compaction notice', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseCommandCodeCompaction(context)
  await page.reload()
  await expectCompactionNotice(page)
})
