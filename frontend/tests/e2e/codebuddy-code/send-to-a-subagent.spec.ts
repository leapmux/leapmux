import { codebuddyTest } from '../codebuddy-fixtures'
import { expectUnsupportedSubagent } from '../helpers/unsupportedSubagent'
import { nativeContext, runningChild } from './scenarios'

codebuddyTest('refuses native child send while the original child task still runs', async ({ codebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: codebuddyWorkspace.workspaceId })
  await expectUnsupportedSubagent(context, { operation: 'send', openChild: () => runningChild(context) })
})
