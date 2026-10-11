import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { opencodeTest } from '../opencode-fixtures'
import { relatedNativeProof } from './scenarios'

opencodeTest('proves the native output-style limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})
