import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

fastAgentTest('exposes no bypass permission shortcut after actual native tool execution', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await expectMissingPermissionShortcut(context, { preset: 'bypass', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
