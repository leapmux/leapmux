import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { qoderTest } from '../qoder-fixtures'
import { nativeContext } from './scenarios'

qoderTest('resolves an actual native permission without a multiline editor request', async ({ askingQoderWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingQoderWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedControl: () => exerciseNativePermissionWrite(context) })
})
