import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { junieTest } from '../junie-fixtures'
import { nativeContext, runningChild } from './scenarios'

junieTest('refuses native child interrupt while the original child task still runs', async ({ authenticatedJunieWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedJunieWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: () => runningChild(context) })
})
