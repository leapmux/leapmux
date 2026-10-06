import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'
import { relatedNativeProof } from './scenarios'

kiloTest('proves the native output-style limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})
