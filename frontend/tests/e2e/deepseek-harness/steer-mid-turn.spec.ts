import { deepseekHarnessTest } from '../deepseek-harness-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { nativeContext } from './scenarios'

deepseekHarnessTest('delivers steering to the current native turn before its real command ends', async ({ deepseekHarnessWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: deepseekHarnessWorkspace.workspaceId })
  await exerciseSteerAfterTool(context, { expectDisplayedOutput: false })
})
