import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { zcodeTest } from '../zcode-fixtures'
import { exerciseZCodeShortcutOffer } from './modeScenario'
import { relatedNativeProof } from './scenarios'

zcodeTest('smart-permissions-shortcut: offers only the bypass permission shortcut', async ({ native }) => {
  await exerciseZCodeShortcutOffer(native)
})

zcodeTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => relatedNativeProof(native) })
})
