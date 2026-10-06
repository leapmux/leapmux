import { geminiTest } from '../gemini-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { nativeContext } from './scenarios'

geminiTest('excludes a smart permission shortcut while native permissions still work', async ({ page, modelScript, leapmuxServer, authenticatedGeminiWorkspace }) => {
  const context = nativeContext({ page, modelScript, leapmuxServer, workspaceId: authenticatedGeminiWorkspace.workspaceId })
  await expectMissingPermissionShortcut(context, { preset: 'smart', relatedProof: () => exerciseNativePermissionWrite(context) })
})
