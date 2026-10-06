import { expectMissingSetting } from '../helpers/unsupportedConfiguration'
import { kiloTest } from '../kilo-fixtures'
import { relatedNativeProof } from './scenarios'

kiloTest('proves the native fast-mode limit after a real sidebar operation', async ({ native }) => {
  await expectMissingSetting(native, { feature: 'fast-mode', relatedProof: () => relatedNativeProof(native) })
})
