import { droidTest } from '../droid-fixtures'
import { exerciseUngroupedNativeChildren } from '../helpers/workflowGrouping'
import { nativeContext, runningChild } from './scenarios'

droidTest('keeps two native child work rows without a workflow group', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await exerciseUngroupedNativeChildren(context, options => runningChild(context, options))
})
