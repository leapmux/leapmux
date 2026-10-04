import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { lettaTest } from '../letta-fixtures'
import { nativeContext, runningChild } from './scenarios'

lettaTest('refuses native child interrupt while the original child task still runs', async ({ authenticatedLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedLettaWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'interrupt', openChild: () => runningChild(context) })
})
