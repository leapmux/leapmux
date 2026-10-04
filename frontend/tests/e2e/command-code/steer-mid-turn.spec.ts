import { commandCodeTest } from '../command-code-fixtures'
import { exerciseSteerAfterTool } from '../helpers/nativeToolSteering'
import { nativeContext } from './scenarios'

commandCodeTest('delivers new text to the actual running native turn', async ({ commandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: commandCodeWorkspace.workspaceId })
  await exerciseSteerAfterTool(context, { expectDisplayedOutput: false })
})
