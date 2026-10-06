import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { zcodeTest } from '../zcode-fixtures'
import { relatedNativeProof } from './scenarios'

zcodeTest('proves the native output-style limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})
