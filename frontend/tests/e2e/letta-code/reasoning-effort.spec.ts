// Native effort updates exist, but the settings catalog exposes no effort control.
// See https://github.com/leapmux/leapmux/issues/501.
import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { lettaTest } from '../letta-fixtures'
import { relatedNativeProof } from './scenarios'

lettaTest('exposes no reasoning-effort control in LeapMux', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'reasoning-effort', relatedProof: () => relatedNativeProof(native) })
})
