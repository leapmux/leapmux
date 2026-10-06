import { commandCodeTest } from '../command-code-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { nativeContext, runningChild } from './scenarios'

commandCodeTest('keeps two actual native child tasks independent before and after reload', async ({ authenticatedCommandCodeWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedCommandCodeWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, options => runningChild(context, options))
})
