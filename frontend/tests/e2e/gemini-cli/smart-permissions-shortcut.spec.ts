import { geminiTest } from '../gemini-fixtures'
import { exerciseNativePermissionWrite } from '../helpers/nativePermission'
import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'

geminiTest('excludes a smart permission shortcut while native permissions still work', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseNativePermissionWrite(native) })
})
