import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { reasonixTest } from '../reasonix-fixtures'
import { relatedNativeProof } from './scenarios'

reasonixTest('proves the native output-style limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})
