import { droidTest } from '../droid-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { nativeContext, runningChild } from './scenarios'

droidTest('refuses native child interrupt while the original child task still runs', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: () => runningChild(context) })
})
