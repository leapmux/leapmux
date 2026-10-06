import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'
import { lettaTest } from '../letta-fixtures'
import { nativeContext } from './scenarios'

lettaTest('resolves an actual native permission without a multiline editor request', async ({ askingLettaWorkspace, page, modelScript, leapmuxServer }) => {
  const context = await nativeContext({ page, modelScript, leapmuxServer, workspaceId: askingLettaWorkspace.workspaceId })
  await expectNoNativeEditorRequest(context, { relatedProof: () => exerciseNativePermissionWrite(context) })
})
