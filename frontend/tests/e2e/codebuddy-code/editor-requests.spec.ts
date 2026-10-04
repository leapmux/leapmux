import { codebuddyTest } from '../codebuddy-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { nativeContext } from './scenarios'

codebuddyTest('resolves an actual native permission without a multiline editor request', async ({ askingCodebuddyWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingCodebuddyWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedControl: () => exerciseNativePermissionWrite(context) })
})
