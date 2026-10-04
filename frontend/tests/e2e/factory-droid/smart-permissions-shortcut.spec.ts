import { droidTest } from '../droid-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

droidTest('exposes no smart permission shortcut after actual native tool execution', async ({ authenticatedDroidWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDroidWorkspace.workspaceId })
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
