import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { nativeContext } from './scenarios'

fastAgentTest('resolves an actual native permission without a multiline editor request', async ({ authenticatedFastAgentWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedFastAgentWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedProof: () => exerciseNativePermissionWrite(context) })
})
