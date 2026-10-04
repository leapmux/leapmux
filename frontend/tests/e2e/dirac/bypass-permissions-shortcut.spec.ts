import { diracTest } from '../dirac-fixtures'
import { exerciseShellToolExecution } from '../helpers/nativeToolExecution'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

diracTest('exposes no bypass permission shortcut after actual native tool execution', async ({ authenticatedDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedDiracWorkspace.workspaceId })
  await expectMissingPermissionShortcut(context, { preset: 'bypass', relatedProof: () => exerciseShellToolExecution(context, { includeFailure: false }) })
})
