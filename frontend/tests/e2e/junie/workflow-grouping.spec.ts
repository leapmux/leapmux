import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { junieTest } from '../junie-fixtures'
import { nativeContext, runningChild } from './scenarios'

junieTest('keeps two native child work rows without a workflow group', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, options => runningChild(context, options))
})
