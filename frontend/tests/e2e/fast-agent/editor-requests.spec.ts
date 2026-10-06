import { fastAgentTest } from '../fastagent-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectNoNativeEditorRequest } from '../helpers/unsupportedEditor'

fastAgentTest('resolves an actual native permission without a multiline editor request', async ({ native }) => {
  await expectNoNativeEditorRequest(native, { relatedProof: () => exerciseNativePermissionWrite(native) })
})
