import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('exposes no bypass permission shortcut after actual native tool execution', async ({ authenticatedQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedQoderWorkspace.workspaceId })
  await expectMissingPermissionShortcut(context, { preset: 'bypass', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
