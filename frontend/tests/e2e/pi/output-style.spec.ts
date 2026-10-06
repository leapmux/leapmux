import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { piTest } from '../pi-fixtures'
import { relatedNativeProof } from './scenarios'

piTest('proves the native output-style limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'output-style', relatedProof: () => relatedNativeProof(native) })
})
