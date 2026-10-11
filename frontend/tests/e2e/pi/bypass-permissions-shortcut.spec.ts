import { expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { piTest } from '../pi-fixtures'
import { relatedNativeProof } from './scenarios'

piTest('proves the native bypass-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'bypass', relatedProof: () => relatedNativeProof(native) })
})
