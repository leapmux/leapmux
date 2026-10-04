import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseFileToolExecution } from '../helpers/nativeToolExecution'
import { nativeContext } from './scenarios'

deepseekHarnessTest('reads and changes actual file bytes through native tools', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseFileToolExecution(context)
})
