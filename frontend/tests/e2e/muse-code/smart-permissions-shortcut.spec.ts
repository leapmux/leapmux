import { exerciseCapabilityProbe, expectMissingPermissionShortcut } from '../helpers/unsupportedConfiguration'
import { museTest } from '../muse-fixtures'

// Muse offers its own approval modes and the bypass preset (allowAll). Its native
// permission vocabulary states no safety-assisted preset, so no smart shortcut exists.
museTest('proves the native smart-permissions-shortcut limit after a real sidebar operation', async ({ native }) => {
  await expectMissingPermissionShortcut(native, { preset: 'smart', relatedProof: () => exerciseCapabilityProbe(native) })
})
