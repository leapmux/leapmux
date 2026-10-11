import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { relatedNativeProof } from './scenarios'

reasonixTest('proves the native fast-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})
