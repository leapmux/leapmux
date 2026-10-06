import { diracTest } from '../dirac-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { nativeContext } from './scenarios'

diracTest('resolves an actual native permission without a multiline editor request', async ({ askingDiracWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingDiracWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedProof: () => exerciseNativePermissionWrite(context) })
})
